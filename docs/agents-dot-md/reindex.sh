#!/usr/bin/env bash
# docs/agents-dot-md/reindex.sh
# 重建 AGENTS.md 里的《项目 Skill 索引》《模块文档索引》两块，并生成 00-index.md。
# 用法：bash docs/agents-dot-md/reindex.sh
# 幂等；只改写 <!-- SKILLS:START/END --> 与 <!-- MODULES:START/END --> 之间的内容。
set -euo pipefail
export LC_ALL=C.UTF-8 2>/dev/null || true

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
AGENTS="$ROOT/AGENTS.md"
ADIR="$ROOT/docs/agents-dot-md"
REL="docs/agents-dot-md"   # ADIR 相对仓库根的路径，用于生成链接

# 截断到首句（。或换行）并限长
trim() { local s="$1"; s="${s%%。*}"; s="${s%%$'\n'*}"; [ "${#s}" -gt 100 ] && s="${s:0:100}…"; printf '%s' "$s"; }

# 列出全仓所有 SKILL.md（basename 精确匹配）。项目常把 skill 埋在各业务模块子目录下，
# 所以不能只扫根 skills/。git 仓只认已跟踪文件（自动排除 bin/、target/ 等产物副本）；
# 非 git 仓退回 find 并剪掉常见产物目录。输出绝对路径。
list_skill_files() {
  if git -C "$ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    git -C "$ROOT" -c core.quotePath=false ls-files | grep -E '(^|/)SKILL\.md$' | sed "s#^#$ROOT/#"
  else
    find "$ROOT" \( -name node_modules -o -name target -o -name build -o -name dist \
      -o -name bin -o -name out -o -name .git -o -name .gradle -o -name vendor \
      -o -name .venv -o -name .idea \) -prune -o -type f -name SKILL.md -print
  fi | sort
}

skills_block() {
  local f name desc dir had=0
  while IFS= read -r f; do
    [ -e "$f" ] || continue
    name="$(sed -n 's/^name:[[:space:]]*//p' "$f" | head -1)"
    desc="$(sed -n 's/^description:[[:space:]]*//p' "$f" | head -1)"
    dir="$(dirname "$f")"; dir="${dir#"$ROOT"/}"   # 相对仓库根，便于定位埋在模块下的 skill
    [ -n "$name" ] || name="$(basename "$(dirname "$f")")"
    printf -- '- **%s** — %s （`%s`）\n' "$name" "$(trim "$desc")" "$dir"
    had=1
  done < <(list_skill_files)
  [ "$had" = 1 ] || printf -- '- （仓库内暂无 SKILL.md）\n'
}

# $1 = 链接前缀（AGENTS.md 在仓库根用 "docs/agents-dot-md/"；00-index.md 与模块同目录用空）
modules_block() {
  local prefix="$1" f base title summary
  for f in "$ADIR"/*.md; do
    [ -e "$f" ] || continue
    base="$(basename "$f")"
    [ "$base" = "00-index.md" ] && continue
    title="$(sed -n 's/^#[[:space:]]*//p' "$f" | head -1)"
    summary="$(sed -n 's/^>[[:space:]]*//p' "$f" | head -1)"
    [ -n "$title" ] || title="$base"
    printf -- '- [%s](%s%s) — %s\n' "$title" "$prefix" "$base" "$summary"
  done
}

# 用新内容替换 <!-- MARK:START --> 与 <!-- MARK:END --> 之间的行
replace_block() {
  local file="$1" mark="$2" content="$3"
  awk -v s="<!-- ${mark}:START -->" -v e="<!-- ${mark}:END -->" -v cf="$content" '
    $0==s {print; while ((getline l < cf) > 0) print l; close(cf); k=1; next}
    $0==e {k=0}
    k==1 {next}
    {print}
  ' "$file" > "$file.tmp" && mv "$file.tmp" "$file"
}

[ -f "$AGENTS" ] || { echo "reindex: 未找到 $AGENTS" >&2; exit 1; }

ts="$(mktemp)"; skills_block > "$ts"
tm="$(mktemp)"; modules_block "$REL/" > "$tm"

replace_block "$AGENTS" SKILLS  "$ts"
replace_block "$AGENTS" MODULES "$tm"

# 生成 00-index.md：它与模块同目录，链接用空前缀（同级引用）
{
  echo "# docs/agents-dot-md 模块索引"
  echo
  echo "> AGENTS.md 的细化模块目录（不会被 Claude Code 自动加载，按需查阅）。本文件由 \`reindex.sh\` 生成，勿手工编辑。"
  echo
  modules_block ""
} > "$ADIR/00-index.md"

echo "reindex done: skills=$(grep -c '^- ' "$ts"), modules=$(grep -c '^- ' "$tm")"
rm -f "$ts" "$tm"
