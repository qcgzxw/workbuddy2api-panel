#!/usr/bin/env bash
# audit_upstream_pr.sh —— 盘点上游**未合并 PR**里有没有我们该吸收的东西。
#
# 为什么需要：上游的修复常先在 PR 分支上存在（甚至永远不合入 master），只盯 master 会漏。
# 但 PR ref 的 head 不是 master 祖先**不代表未合并**（squash-merge 后 head 永远不是祖先），
# 所以判断同样只能靠内容：该 PR 相对 merge-base 的新增行，是否已在 master / 已在本仓库。
#
# 判定口径
#   master缺 ≈ 0        → 该 PR 内容已进 master（squash-merge），跳过
#   master缺 大 + 我们缺 大 → 真未合并且我们也没接 → 候选
#   master缺 大 + 我们缺 小 → 未合并但本仓库已自行实现/吸收 → 一般无需动作
#   新增文件（"本仓库无的文件"）全量计入缺失，避免"新文件多的 PR"被误判为已合并。
#
# 用法
#   scripts/audit_upstream_pr.sh                        # 默认 upstream2 + master
#   scripts/audit_upstream_pr.sh --fetch                # 先抓取 PR refs 再盘点
#   scripts/audit_upstream_pr.sh --remote upstream1 --branch main
#
# 注意：只抓 refs/pull/*/head，不抓 merge refs；上游仓库 PR 多时首次抓取较慢。
set -uo pipefail

REMOTE=upstream2
BRANCH=master
DO_FETCH=0
MAX_FILES=4   # 每行最多列出几个"本仓库无的文件"

while [ $# -gt 0 ]; do
    case "$1" in
        --remote) REMOTE="$2"; shift 2 ;;
        --branch) BRANCH="$2"; shift 2 ;;
        --fetch)  DO_FETCH=1; shift ;;
        -h|--help) sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "未知参数: $1（-h 看用法）" >&2; exit 2 ;;
    esac
done

git rev-parse --verify -q "$REMOTE/$BRANCH^{commit}" >/dev/null \
    || { echo "上游分支不存在: $REMOTE/$BRANCH（先 git fetch $REMOTE）" >&2; exit 1; }

if [ "$DO_FETCH" -eq 1 ]; then
    echo "抓取 $REMOTE 的 PR refs（首次较慢）..."
    git fetch "$REMOTE" '+refs/pull/*/head:refs/remotes/'"$REMOTE"'/pr/*' || exit 1
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# count_missing <ref> <file> <added-file> <out-new-file-marker>
# 输出：缺失行数（文件不存在 → 全部算缺失，并打印 "NEW"）
count_missing() {
    local ref="$1" file="$2" added="$3"
    if ! git cat-file -e "$ref:$file" 2>/dev/null; then
        sort -u "$added" | grep -ac .
        return
    fi
    git show "$ref:$file" > "$TMP/target.txt"
    comm -23 <(sort -u "$added") <(sort -u "$TMP/target.txt") | grep -c .
}

found=0
while read -r ref; do
    [ -z "$ref" ] && continue
    head_rev=$(git rev-parse "$ref")
    git merge-base --is-ancestor "$head_rev" "$REMOTE/$BRANCH" 2>/dev/null && continue
    base=$(git merge-base "$REMOTE/$BRANCH" "$head_rev" 2>/dev/null) || continue

    files=$(git diff --name-only "$base" "$head_rev")
    [ -z "$files" ] && continue

    add_sum=0; master_miss=0; ours_miss=0; ours_code=0; new_files=""
    while read -r f; do
        [ -z "$f" ] && continue
        added="$TMP/added.txt"
        git diff "$base" "$head_rev" -- "$f" | sed -n 's/^+//p' | grep -av '^++' > "$added"
        n=$(sort -u "$added" | grep -ac .)
        [ "$n" -eq 0 ] && continue
        add_sum=$((add_sum + n))
        master_miss=$((master_miss + $(count_missing "$REMOTE/$BRANCH" "$f" "$added")))
        ours=$(count_missing HEAD "$f" "$added")
        ours_miss=$((ours_miss + ours))
        # 测试文件与 .github 单独归类：本仓库测试覆盖本就与上游不同，测试行缺失
        # 会虚高"我们缺"，判读要看**代码**缺口。
        case "$f" in
            *_test.go|*/tests/*|.github/*) ;;
            *) ours_code=$((ours_code + ours)) ;;
        esac
        if ! git cat-file -e HEAD:"$f" 2>/dev/null; then
            new_files="$new_files ${f##*/}"
        fi
    done <<< "$files"

    # 内容已进 master（squash-merge）→ 不是候选，只在与本仓库有落差时提一句
    [ "$add_sum" -eq 0 ] && continue
    if [ "$master_miss" -eq 0 ] && [ "$ours_miss" -eq 0 ]; then continue; fi

    found=$((found + 1))
    pr=${ref##*/}
    nonhub=$(printf '%s\n' "$files" | grep -v '^\.github/' | wc -l)
    printf '%-8s %s  提交数=%-3s add=%-5s master缺=%-5s 我们缺=%-5s(代码 %-4s)\n' \
        "pr/$pr" "$(git log -1 --format=%ad --date=short "$head_rev")" \
        "$(git rev-list --count "$base..$head_rev")" "$add_sum" "$master_miss" "$ours_miss" "$ours_code"
    newlist=$(printf '%s\n' "$new_files" | tr ' ' '\n' | grep -av '^$')
    printf '         非 .github 文件 %s 个' "$nonhub"
    if [ -n "$newlist" ]; then
        printf '；本仓库无：%s' "$(printf '%s\n' "$newlist" | head -"$MAX_FILES" | tr '\n' ' ')"
        total_new=$(printf '%s\n' "$newlist" | grep -ac .)
        [ "$total_new" -gt "$MAX_FILES" ] && printf '…共 %s 个' "$total_new"
    fi
    printf '\n'
done < <(git for-each-ref --format='%(refname)' "refs/remotes/$REMOTE/pr/" | sort -t/ -k4 -n)

echo
echo "候选 PR：$found 条（已按 PR 号排序；'master缺≈0' 的已被排除）"
echo "判读：master缺 大 + 我们缺 大 = 真未合并且我们也没接；我们缺 小 = 本仓库已自行实现。"
