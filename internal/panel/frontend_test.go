package panel

import (
	"bytes"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAppJSSyntax app.js 必须能通过 JS 解析器语法校验。
//
// 为什么需要：app.js 是 go:embed 进二进制的静态资源，Go 编译器不检查其内容——
// 一次对象字面量键名未加引号（Model_chat_GLM5.2 被解析成属性访问 + 数字字面量）
// 就让整个面板白屏，而所有 Go 测试依然全绿。此测试把语法校验前移到 CI。
// 无 node 环境时跳过（不阻塞无 Node 的构建机）。
func TestAppJSSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; skipping JS syntax check")
	}
	path, err := filepath.Abs("app.js")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "--check", path).CombinedOutput()
	if err != nil {
		t.Fatalf("app.js syntax error:\n%s", out)
	}
}

// TestIndexHTMLNoInlineScript index.html 不得含内联 <script> 块：
// 严格 CSP（script-src 'self'）会拦截内联脚本，页面将完全不可用。
// 外链形式 <script src="..."> 允许。
func TestIndexHTMLNoInlineScript(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	rest := body
	for {
		idx := strings.Index(rest, "<script")
		if idx < 0 {
			break
		}
		rest = rest[idx:]
		end := strings.Index(rest, ">")
		if end < 0 {
			break
		}
		tag := rest[:end+1]
		if !strings.Contains(tag, "src=") {
			t.Fatalf("index.html contains inline <script> (blocked by CSP): %s", tag)
		}
		rest = rest[end:]
	}
}

// TestIndexHTMLAddAccountTabs 「添加账号」弹窗的两个标签面板必须齐全。
//
// 上面的 JS 测试用的是自建 DOM 桩，index.html 里 id 拼错它照样通过——而拼错的后果
// 是 getElementById 返回 null、整段交互静默失效。这条断言把两边的契约钉在一起。
func TestIndexHTMLAddAccountTabs(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	for _, id := range []string{"addTabs", "addTabLogin", "addTabImport", "importFile", "importDone", "importErr"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("index.html 缺少 id=%q（app.js 会 getElementById 拿到 null，交互静默失效）", id)
		}
	}
	// 两个标签按钮的 data-tab 必须与 switchAddTab 的取值一致。
	for _, tab := range []string{`data-tab="login"`, `data-tab="import"`} {
		if !strings.Contains(body, tab) {
			t.Errorf("index.html 缺少 %s 标签按钮", tab)
		}
	}
	// 导入面板默认隐藏（默认落在登录标签）。
	if !strings.Contains(body, `id="addTabImport" class="tab-panel" hidden`) {
		t.Error("导入面板应默认 hidden")
	}
}

// TestAppJSTopLevelSmoke app.js 顶层求值冒烟：node + DOM 桩真正**执行** app.js
// （含按 hash 落到各视图的 go() 顶层调用），抓 TDZ / ReferenceError 类运行时错误。
//
// 为什么需要：TestAppJSSyntax 只做语法解析，Go 侧 frontend_test 不执行 JS——
// 「let 声明在调用点之后」这类 TDZ 崩溃语法完全合法，node --check 也通过，
// 但整个脚本会在求值中途抛错停止，后续声明全部未初始化，页面白屏。
// 上游 fork 曾因此连炸两个版本（删函数漏调用点 / reattachQueueView 同步读
// 后声明的 let）。此测试把运行时闸门前移到 CI。
//
// harness 与 app.js 同判：app.js 顶层 start() 的 setInterval 会让 node 事件
// 循环不退出，故成功路径显式 exit(0)；fetch 返回永不 resolve 的 Promise，
// 让所有异步视图加载停在第一个 await，只检验顶层同步求值段。
// 无 node 环境时跳过（不阻塞无 Node 的构建机）。
func TestAppJSTopLevelSmoke(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; JS smoke skipped")
	}
	harness := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
// inert：万能 DOM 桩。任何属性读取/调用/构造都返回自身，故 $('x').onclick = f
// 之类的写路径全部静默成功；String/Symbol.toPrimitive 返回空串，让拼接与比较不抛。
const inert = new Proxy(function () {}, {
  get(t, k) { if (k === Symbol.toPrimitive) return () => ''; return inert; },
  set() { return true; },
  apply() { return inert; },
  construct() { return inert; },
  has() { return true; },
});
const sandbox = new Proxy({
  location: { hash: process.env.SMOKE_HASH || '#accounts' },
  history: { replaceState() {} },
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  navigator: { clipboard: { writeText: () => Promise.resolve() } },
  document: { querySelectorAll: () => [], querySelector: () => inert, getElementById: () => inert, addEventListener() {}, documentElement: inert, head: inert, body: inert, createElement: () => inert, cookie: '' },
  fetch: () => new Promise(() => {}),
  addEventListener() {}, removeEventListener() {},
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  setInterval, clearInterval, setTimeout, clearTimeout,
  getComputedStyle: () => inert,
  confirm: () => false, prompt: () => null, alert() {},
  console, JSON, Math, Date, Number, String, Boolean, Object, Array, Promise, Map, Set, WeakMap, WeakSet, RegExp, Error, TypeError, RangeError, isNaN, isFinite, parseInt, parseFloat,
  encodeURIComponent, decodeURIComponent, encodeURI, decodeURI, escape, unescape,
  URL, URLSearchParams, TextEncoder, TextDecoder, Blob, FormData, Symbol, Proxy, Reflect, Intl,
  ArrayBuffer, Uint8Array, Uint16Array, Uint32Array, Int8Array, Int16Array, Int32Array, Float64Array, DataView,
}, { get(t, k) { return t[k]; }, has() { return true; } });
sandbox.window = sandbox; sandbox.globalThis = sandbox;
vm.createContext(sandbox);
try {
  vm.runInContext(src, sandbox, { filename: 'app.js' });
  console.log('SMOKE OK');
  process.exit(0);
} catch (e) {
  console.log('SMOKE FAIL:', (e && e.stack ? e.stack : e).toString().split('\n').slice(0, 5).join('\n'));
  process.exit(1);
}
`
	hf, err := os.CreateTemp(t.TempDir(), "smoke-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hf.WriteString(harness); err != nil {
		t.Fatal(err)
	}
	hf.Close()
	// 七个视图各落一次：go() 的每个分支都会同步调用对应的 loadXxx，覆盖
	// 「顶层调用 → 后声明的 let/const」全部组合。
	for _, hash := range []string{"#accounts", "#usage", "#packages", "#taskscenter", "#models", "#config", "#logs"} {
		cmd := exec.Command(node, hf.Name(), "app.js")
		cmd.Dir = "." // 测试工作目录 = internal/panel
		cmd.Env = append(os.Environ(), "SMOKE_HASH="+hash)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("app.js 顶层求值 %s 崩溃: %v\n%s", hash, err, out)
		}
		if !bytes.Contains(out, []byte("SMOKE OK")) {
			t.Fatalf("app.js smoke %s 未通过:\n%s", hash, out)
		}
	}
}

// TestAddAccountTabsSwitch 「添加账号」弹窗的标签切换行为（真实执行 app.js）。
//
// 为什么不能只靠 Go 侧断言：标签切换与底部按钮可见性是**交互**逻辑，Go 测试读不到。
// 这里给 node 一个带真实元素的最小 DOM（getElementById 返回可写对象），执行 app.js
// 后调 openAdd/switchAddTab 并断言各元素的 hidden 状态。
//
// 重点覆盖：导入标签下必须隐藏三个登录动作按钮——否则点「获取授权链接」会写进
// 隐藏面板里的 addReady，用户看不到任何反馈（静默失效）。
func TestAddAccountTabsSwitch(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; tab switch test skipped")
	}
	// harness 负责搭 DOM 与执行 app.js；断言脚本经环境变量传入（避免 Go 原始字符串
	// 里嵌套反引号截断字面量），在同一个 vm 上下文里跑，故能直接调 app.js 的顶层函数。
	harness := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const inert = new Proxy(function () {}, {
  get(t, k) { if (k === Symbol.toPrimitive) return () => ''; return inert; },
  set() { return true; }, apply() { return inert; }, construct() { return inert; }, has() { return true; },
});
function el(id) {
  const e = { id, hidden: false, disabled: false, value: '', textContent: '', files: [], dataset: {}, _cls: new Set() };
  e.classList = {
    add: c => e._cls.add(c),
    remove: c => e._cls.delete(c),
    contains: c => e._cls.has(c),
    toggle: (c, force) => {
      if (force === undefined) { e._cls.has(c) ? e._cls.delete(c) : e._cls.add(c); }
      else if (force) { e._cls.add(c); } else { e._cls.delete(c); }
    },
  };
  return e;
}
// 初始可见性对齐 index.html 的标记：登录面板可见、导入面板 hidden、复制/打开 hidden。
const els = {};
for (const id of ['addPick','addLoad','addReady','addDone','addErr','importDone','importErr',
                  'addTabLogin','addTabImport','btnStartLogin','btnCopyUrl','btnOpenUrl','importFile']) els[id] = el(id);
els.addReady.hidden = true;
els.addTabImport.hidden = true;
els.btnCopyUrl.hidden = true;
els.btnOpenUrl.hidden = true;
els.importDone.hidden = true;
els.importErr.hidden = true;
const tabs = [el('tab-login'), el('tab-import')];
tabs[0].dataset.tab = 'login'; tabs[0]._cls.add('on');
tabs[1].dataset.tab = 'import';

const sandbox = new Proxy({
  __els: els, __tabs: tabs,
  location: { hash: '#accounts' }, history: { replaceState() {} },
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
  navigator: { clipboard: { writeText: () => Promise.resolve() } },
  document: {
    getElementById: id => els[id] || inert,
    querySelectorAll: sel => (sel === '#addTabs .tab' ? tabs : []),
    querySelector: () => inert,
    addEventListener() {}, documentElement: inert, head: inert, body: inert,
    createElement: () => inert, cookie: '',
  },
  fetch: () => new Promise(() => {}),
  addEventListener() {}, removeEventListener() {},
  matchMedia: () => ({ matches: false, addEventListener() {} }),
  setInterval, clearInterval, setTimeout, clearTimeout,
  getComputedStyle: () => inert, confirm: () => false, prompt: () => null, alert() {},
  console, JSON, Math, Date, Number, String, Boolean, Object, Array, Promise, Map, Set, WeakMap, WeakSet,
  RegExp, Error, TypeError, RangeError, isNaN, isFinite, parseInt, parseFloat,
  encodeURIComponent, decodeURIComponent, encodeURI, decodeURI, escape, unescape,
  URL, URLSearchParams, TextEncoder, TextDecoder, Blob, FormData, Symbol, Proxy, Reflect, Intl,
  ArrayBuffer, Uint8Array, Uint16Array, Uint32Array, Int8Array, Int16Array, Int32Array, Float64Array, DataView,
}, { get(t, k) { return t[k]; }, has() { return true; } });
sandbox.window = sandbox; sandbox.globalThis = sandbox;
vm.createContext(sandbox);
try {
  vm.runInContext(src, sandbox, { filename: 'app.js' });
  const fails = vm.runInContext(process.env.ASSERT_SRC, sandbox);
  if (fails.length) {
    console.log('TAB FAIL:\n' + fails.join('\n'));
    process.exit(1);
  }
  console.log('TAB OK');
  process.exit(0);
} catch (e) {
  console.log('TAB ERROR:', (e && e.stack ? e.stack : e).toString().split('\n').slice(0, 5).join('\n'));
  process.exit(1);
}
`
	// 断言脚本在 app.js 的上下文里执行，故能直接调 openAdd / switchAddTab。
	assertSrc := `(function () {
  const f = [];
  const E = id => __els[id];
  const on = i => __tabs[i]._cls.has('on');
  const chk = (cond, msg) => { if (!cond) f.push(msg); };

  openAdd();
  chk(E('addTabLogin').hidden === false, 'openAdd 后应显示登录面板');
  chk(E('addTabImport').hidden === true, 'openAdd 后应隐藏导入面板');
  chk(E('btnStartLogin').hidden === false, '初始应显示「获取授权链接」');
  chk(E('btnCopyUrl').hidden === true, '初始应隐藏「复制链接」');
  chk(on(0) === true && on(1) === false, '初始应只高亮登录标签');

  // 点「导入 JSON」标签
  __tabs[1].onclick();
  chk(E('addTabImport').hidden === false, '导入标签应显示导入面板');
  chk(E('addTabLogin').hidden === true, '导入标签应隐藏登录面板');
  chk(on(1) === true && on(0) === false, '应只高亮导入标签');
  chk(E('btnStartLogin').hidden === true, '导入标签下应隐藏「获取授权链接」（否则点了写进隐藏面板，静默失效）');
  chk(E('btnCopyUrl').hidden === true, '导入标签下应隐藏「复制链接」');
  chk(E('btnOpenUrl').hidden === true, '导入标签下应隐藏「在浏览器打开」');

  // 授权链接已就绪时切走再切回：就绪态不得丢
  E('addReady').hidden = false; // 等价 startAddLogin 的成功回调
  __tabs[1].onclick();
  __tabs[0].onclick();
  chk(E('addTabLogin').hidden === false, '切回登录应显示登录面板');
  chk(E('btnCopyUrl').hidden === false, '切回后应恢复「复制链接」');
  chk(E('btnOpenUrl').hidden === false, '切回后应恢复「在浏览器打开」');
  chk(E('btnStartLogin').hidden === true, '会话在途时不应再显示「获取授权链接」');

  // 重新打开弹窗应回到干净的初始态（上一轮残留不得带进来）
  openAdd();
  chk(E('btnStartLogin').hidden === false, 'openAdd 应复位到「获取授权链接」');
  chk(E('btnCopyUrl').hidden === true, 'openAdd 应隐藏「复制链接」');
  chk(E('addTabImport').hidden === true, 'openAdd 应回到登录标签');
  chk(on(0) === true && on(1) === false, 'openAdd 应复位标签高亮');
  return f;
})()`

	hf, err := os.CreateTemp(t.TempDir(), "tabs-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hf.WriteString(harness); err != nil {
		t.Fatal(err)
	}
	hf.Close()

	cmd := exec.Command(node, hf.Name(), "app.js")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(), "ASSERT_SRC="+assertSrc)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("标签切换测试失败: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("TAB OK")) {
		t.Fatalf("标签切换未通过:\n%s", out)
	}
}
