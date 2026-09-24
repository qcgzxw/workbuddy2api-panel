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
