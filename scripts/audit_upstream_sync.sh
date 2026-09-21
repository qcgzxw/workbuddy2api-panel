#!/usr/bin/env bash
# audit_upstream_sync.sh —— 核对「我们声称吸收的上游区间」是否真的一条不漏。
#
# 为什么需要：本仓库与上游没有共同祖先（历来手工搬运代码，不是 merge），
# git merge-base --is-ancestor 一律返回"未合入"，无法判断某个上游提交是否已吸收。
# 唯一可靠的判据是**内容**：该提交新增/修改的行，是否已存在于本仓库对应文件里。
#
# 典型用法
#   scripts/audit_upstream_sync.sh                 # 自动取最近一条同步提交声称的区间左端
#   scripts/audit_upstream_sync.sh --base b08f518  # 显式指定起点
#   scripts/audit_upstream_sync.sh --upstream upstream1/main --base 1948484
#
# 输出
#   每行一条上游提交：add=该提交非 .github 改动的新增行（去重）/miss=本仓库缺失的行数
#   miss=0 → 内容已在（或等价实现已存在）；miss>0 → 需要人工看 diff 判断是"真缺"还是
#   "本仓库自有实现"，末尾汇总列出 miss>0 的提交。
#
# 已知边界（别把 miss 当结论，当线索）
#   - 行级存在性判据对**本仓库已重写/改名**的代码会误报 miss（上游同一修复用另一种写法落地）。
#   - 只统计非 .github 路径（本仓库无 .github，治理流水线一律跳过，末尾给出跳过条数）。
#   - 按"去重后的行"比较，不按重复次数。
set -uo pipefail

UPSTREAM=upstream2/master
BASE=""
VERBOSE=0

while [ $# -gt 0 ]; do
    case "$1" in
        --upstream) UPSTREAM="$2"; shift 2 ;;
        --base)     BASE="$2"; shift 2 ;;
        -v|--verbose) VERBOSE=1; shift ;;
        -h|--help) sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "未知参数: $1（-h 看用法）" >&2; exit 2 ;;
    esac
done

git rev-parse --verify -q "$UPSTREAM^{commit}" >/dev/null \
    || { echo "上游 ref 不存在: $UPSTREAM（先 git fetch）" >&2; exit 1; }

# ── 起点自动探测：最近一条提交信息里含 "xxxxxxx..yyyyyyy" 的同步提交，取其左端 ──
# 取左端（而非右端）是有意的：把"声称已吸收"的整段重新核一遍——历史事故
# e68e139 声称吸收 b08f518..d1023f3，右端对得上、区间内却漏了 4 条。
if [ -z "$BASE" ]; then
    claim_line=$(git log --format='%h %s' -n 300 | grep -E '[0-9a-f]{7,}\.\.[0-9a-f]{7,}' | head -1)
    if [ -z "$claim_line" ]; then
        echo "无法自动探测起点（历史里找不到 'X..Y' 形式的同步区间），请用 --base 指定。" >&2
        exit 1
    fi
    BASE=$(printf '%s' "$claim_line" | grep -oE '[0-9a-f]{7,}\.\.[0-9a-f]{7,}' | head -1 | cut -d. -f1)
    echo "起点自动探测：$BASE"
    echo "  来源提交：$claim_line"
    echo "  （取区间左端，即把该提交声称的整段重新核对）"
fi

git rev-parse --verify -q "$BASE^{commit}" >/dev/null \
    || { echo "起点提交不存在: $BASE" >&2; exit 1; }
git merge-base --is-ancestor "$BASE" "$UPSTREAM" 2>/dev/null \
    || echo "注意：$BASE 不是 $UPSTREAM 的祖先，区间可能取错。"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# added_lines <commit> <file> → stdout: 该提交对该文件新增的行（去掉 +++ 头）
added_lines() { git show "$1" -- "$2" | sed -n 's/^+//p' | grep -av '^++'; }

# missing_count <ref> <file> <added-file> → stdout: added 中不在 ref:file 里的去重行数
# 文件在 ref 中不存在 → 全部算缺失（输出 missing=total）
missing_count() {
    local ref="$1" file="$2" added="$3"
    local present="$TMP/target.txt"
    if ! git cat-file -e "$ref:$file" 2>/dev/null; then
        sort -u "$added" | grep -ac .
        return
    fi
    git show "$ref:$file" > "$present"
    comm -23 <(sort -u "$added") <(sort -u "$present") | grep -c .
}

printf '%-9s %-11s %-6s %-6s %-7s %s\n' "提交" "日期" "add" "miss" "缺失比" "判定 / 标题 / 缺失文件"
printf '%s\n' "------------------------------------------------------------------------------------------"

total=0; clean=0; dirty=0; skipped=0
list_clean=""; list_maybe=""; list_missing=""
while read -r h; do
    [ -z "$h" ] && continue
    files=$(git show --name-only --format= "$h" | grep -v '^$')
    codefiles=$(printf '%s\n' "$files" | grep -v '^\.github/' || true)
    if [ -z "$codefiles" ]; then skipped=$((skipped + 1)); continue; fi

    add_sum=0; miss_sum=0; detail=""
    while read -r f; do
        [ -z "$f" ] && continue
        added="$TMP/added.txt"
        added_lines "$h" "$f" > "$added"
        n=$(sort -u "$added" | grep -ac .)
        [ "$n" -eq 0 ] && continue
        m=$(missing_count HEAD "$f" "$added")
        add_sum=$((add_sum + n)); miss_sum=$((miss_sum + m))
        [ "$m" -gt 0 ] && detail="$detail $f(缺$m/$n)"
    done <<< "$codefiles"

    total=$((total + 1))
    pct=0
    [ "$add_sum" -gt 0 ] && pct=$((miss_sum * 100 / add_sum))
    # 判定阈值 50%：miss 比例高 → 基本是"整块没进来"；低 → 本仓库大概率已重写/等价实现。
    if [ "$miss_sum" -eq 0 ]; then
        verdict="已在"; clean=$((clean + 1)); list_clean="$list_clean$h "
    elif [ "$pct" -ge 50 ]; then
        verdict="真缺?"; dirty=$((dirty + 1)); list_missing="$list_missing$h"
        [ "$VERBOSE" -eq 1 ] && list_missing="$list_missing $detail"
        list_missing="$list_missing"$'\n'
    else
        verdict="重写?"; dirty=$((dirty + 1)); list_maybe="$list_maybe$h"
        [ "$VERBOSE" -eq 1 ] && list_maybe="$list_maybe $detail"
        list_maybe="$list_maybe"$'\n'
    fi
    printf '%-9s %-11s %-6s %-6s %-7s %s %s%s\n' "$h" \
        "$(git log -1 --format=%ad --date=short "$h")" "$add_sum" "$miss_sum" "${pct}%" \
        "$verdict" "$(git log -1 --format=%s "$h" | cut -c1-44)" "$detail"
done < <(git log --no-merges --reverse --format='%h' "$BASE..$UPSTREAM")

echo
printf '区间 %s..%s：非 .github 提交 %d 条（已在 %d，需人工看 %d），仅改 .github 跳过 %d 条\n' \
    "$BASE" "$UPSTREAM" "$total" "$clean" "$dirty" "$skipped"
if [ -n "$list_missing" ]; then
    echo "真缺?（缺失比 ≥50%，按整块没进来处理）："
    printf '%s' "$list_missing" | sed 's/^/  /'
fi
if [ -n "$list_maybe" ]; then
    echo "重写?（有缺失但比例低，本仓库大概率已有等价实现，扫一眼即可）："
    printf '%s' "$list_maybe" | sed 's/^/  /'
fi
if [ "$dirty" -gt 0 ]; then
    echo '提示：miss 只是线索。先看该提交改的文件在本仓库是否存在、是否已有等价实现，'
    echo '      再决定吸收/明确跳过；跳过项写进同步记录「已吸收到 X，跳过 Y（原因）」——'
    echo '      只记区间右端会重演 e68e139 的漏项。'
fi
