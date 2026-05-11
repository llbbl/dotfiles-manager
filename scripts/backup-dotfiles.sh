#!/usr/bin/env bash
# backup-dotfiles.sh
#
# One-shot tar snapshot of the user's dotfiles, intended as a manual
# "pre-manager" backup before this project starts mutating real files.
#
# Output: ~/Backups/dotfiles-pre-manager-<YYYYMMDD-HHMMSS>.tgz
#
# Strategy: explicit allowlist. ~/.config is intentionally skipped
# because it tends to collect a lot of unrelated cache/junk. Add to the
# list either by editing the DEFAULT_PATHS array below, or by creating
# a sidecar file scripts/backup-include.txt with one path per line
# (lines starting with # are comments).

set -u
set -o pipefail

# ---- config ---------------------------------------------------------------

OUT_DIR="${BACKUP_DIR:-$HOME/Backups}"
STAMP="$(date +%Y%m%d-%H%M%S)"
OUT_FILE="$OUT_DIR/dotfiles-pre-manager-$STAMP.tgz"

DEFAULT_PATHS=(
  # shell rc / login
  "$HOME/.zshrc"
  "$HOME/.zshenv"
  "$HOME/.zprofile"
  "$HOME/.zlogin"
  "$HOME/.zlogout"
  "$HOME/.bashrc"
  "$HOME/.bash_profile"
  "$HOME/.bash_logout"
  "$HOME/.profile"
  "$HOME/.inputrc"

  # local-overrides (often hand-maintained)
  "$HOME/.zshrc.local"
  "$HOME/.zshenv.local"
  "$HOME/.aliases"
  "$HOME/.functions"
  "$HOME/.exports"

  # git
  "$HOME/.gitconfig"
  "$HOME/.gitconfig.local"
  "$HOME/.gitignore_global"

  # editors / multiplexer
  "$HOME/.vimrc"
  "$HOME/.tmux.conf"
  "$HOME/.editorconfig"

  # tool/runtime version pins
  "$HOME/.tool-versions"

  # http / api
  "$HOME/.curlrc"
  "$HOME/.wgetrc"
  "$HOME/.netrc"

  # language runtimes
  "$HOME/.npmrc"
  "$HOME/.gemrc"
  "$HOME/.irbrc"
  "$HOME/.pythonrc"

  # ssh / cloud (CONFIGS ONLY — no private keys)
  "$HOME/.ssh/config"
  "$HOME/.aws/config"
  "$HOME/.docker/config.json"

  # ~/.config: only specifically-named subtrees, never the whole dir
  "$HOME/.config/git"
  "$HOME/.config/mise"
  "$HOME/.config/nvim"
  "$HOME/.config/starship.toml"
)

INCLUDE_FILE="$(dirname "${BASH_SOURCE[0]}")/backup-include.txt"

# ---- collect candidate paths ---------------------------------------------

CANDIDATES=("${DEFAULT_PATHS[@]}")

if [[ -f "$INCLUDE_FILE" ]]; then
  while IFS= read -r line; do
    # strip comments + whitespace
    line="${line%%#*}"
    line="${line%"${line##*[![:space:]]}"}"
    line="${line#"${line%%[![:space:]]*}"}"
    [[ -z "$line" ]] && continue
    # expand leading ~/
    [[ "$line" == "~/"* ]] && line="${HOME}/${line:2}"
    CANDIDATES+=("$line")
  done < "$INCLUDE_FILE"
fi

# ---- filter to paths that actually exist ---------------------------------

EXISTING=()
MISSING=()
for p in "${CANDIDATES[@]}"; do
  if [[ -e "$p" ]]; then
    EXISTING+=("$p")
  else
    MISSING+=("$p")
  fi
done

if [[ ${#EXISTING[@]} -eq 0 ]]; then
  echo "no candidate paths exist; nothing to back up" >&2
  exit 1
fi

# ---- run tar -------------------------------------------------------------

mkdir -p "$OUT_DIR"

echo "backing up ${#EXISTING[@]} path(s) to $OUT_FILE"
printf '  + %s\n' "${EXISTING[@]}"
if [[ ${#MISSING[@]} -gt 0 ]]; then
  echo "skipping ${#MISSING[@]} missing path(s):"
  printf '  - %s\n' "${MISSING[@]}"
fi
echo

# Use -C / so absolute paths inside the archive stay absolute and round-trip
# cleanly on restore. GNU tar warns about leading '/'; BSD tar (macOS) is fine.
tar -czf "$OUT_FILE" "${EXISTING[@]}" 2>/dev/null
TAR_RC=$?

if [[ $TAR_RC -ne 0 ]]; then
  echo "tar failed with exit $TAR_RC" >&2
  rm -f "$OUT_FILE"
  exit "$TAR_RC"
fi

# ---- verify --------------------------------------------------------------

if ! tar -tzf "$OUT_FILE" >/dev/null 2>&1; then
  echo "archive verify failed: $OUT_FILE" >&2
  exit 2
fi

SIZE="$(du -h "$OUT_FILE" | awk '{print $1}')"
COUNT="$(tar -tzf "$OUT_FILE" | wc -l | tr -d ' ')"

echo "ok"
echo "  archive: $OUT_FILE ($SIZE, $COUNT entries)"
echo
ls -lh "$OUT_DIR" | tail -n +2
