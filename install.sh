#!/bin/sh
# Install kite and kite-agent on Linux or macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/zzzbug01/kite/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/zzzbug01/kite/main/install.sh | sh -s -- --agent --relay tunnel.example.com --code ABCD-EFGH
#
# KITE_VERSION (or --version) pins a tag such as v0.1.0-rc.3.
# --relay-server also installs kite-relay, and only on Linux.
# KITE_GITHUB_API and KITE_RELEASE_BASE override the download locations.
# KITE_BIN_DIR and KITE_NO_SUDO are test hooks.
set -eu

repo="zzzbug01/kite"
api_root="${KITE_GITHUB_API:-https://api.github.com}"
release_base="${KITE_RELEASE_BASE:-https://github.com/${repo}/releases/download}"

version="${KITE_VERSION:-}"
agent=0
relay=""
code=""
relay_server=0
service_name="${KITE_SERVICE_NAME:-}"

usage() {
  echo "Usage: install.sh [--version vX] [--agent --relay HOST --code ABCD-EFGH] [--relay-server]" >&2
  echo "Installs kite and kite-agent. --agent also runs kite-agent install." >&2
  echo "kite-relay is installed only on Linux, and only with --relay-server." >&2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version)
      shift
      if [ $# -lt 1 ]; then
        echo "install.sh: --version needs a value" >&2
        exit 1
      fi
      version=$1
      shift
      ;;
    --version=*)
      version=${1#--version=}
      shift
      ;;
    --agent)
      agent=1
      shift
      ;;
    --relay)
      shift
      if [ $# -lt 1 ]; then
        echo "install.sh: --relay needs a value" >&2
        exit 1
      fi
      relay=$1
      shift
      ;;
    --relay=*)
      relay=${1#--relay=}
      shift
      ;;
    --code)
      shift
      if [ $# -lt 1 ]; then
        echo "install.sh: --code needs a value" >&2
        exit 1
      fi
      code=$1
      shift
      ;;
    --code=*)
      code=${1#--code=}
      shift
      ;;
    --relay-server)
      relay_server=1
      shift
      ;;
    --service-name)
      shift
      if [ $# -lt 1 ]; then
        echo "install.sh: --service-name needs a value" >&2
        exit 1
      fi
      service_name=$1
      shift
      ;;
    --service-name=*)
      service_name=${1#--service-name=}
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "install.sh: unknown argument $1" >&2
      usage
      exit 1
      ;;
  esac
done

case "$version" in
  ""|v*) ;;
  [0-9]*) version="v$version" ;;
esac

# --code is required only for a new instance. An existing instance can be
# upgraded with --agent and no code; a code re-redeems the token.

os=$(uname -s)
case "$os" in
  Linux) goos=linux ;;
  Darwin) goos=darwin ;;
  *)
    echo "install.sh: unsupported operating system $os" >&2
    exit 1
    ;;
esac

machine=$(uname -m)
case "$machine" in
  x86_64|amd64) goarch=amd64 ;;
  aarch64|arm64) goarch=arm64 ;;
  *)
    echo "install.sh: unsupported architecture $machine" >&2
    exit 1
    ;;
esac

if [ "$relay_server" -eq 1 ] && [ "$goos" != "linux" ]; then
  echo "install.sh: kite-relay is installed on Linux. This machine is $goos." >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "install.sh: curl is required" >&2
  exit 1
fi

# Newest GitHub release. A stable tag wins. Prereleases are used only when
# every published release is a prerelease. The API lists newest first;
# tag_name is emitted before draft and prerelease in each object.
resolve_version() {
  body=$(curl -fsSL --retry 3 --retry-delay 1 -A kite-install \
    -H "Accept: application/vnd.github+json" \
    "${api_root}/repos/${repo}/releases?per_page=30") || {
    echo "install.sh: could not list releases" >&2
    exit 1
  }
  # Split on commas so a compact JSON document still has one field per line.
  picked=$(printf '%s\n' "$body" | tr ',' '\n' | awk '
    /"tag_name"[[:space:]]*:/ {
      tag = $0
      sub(/^[^"]*"[^"]*"[[:space:]]*:[[:space:]]*"/, "", tag)
      sub(/".*/, "", tag)
      draft = 0
    }
    /"draft"[[:space:]]*:/ { draft = ($0 ~ /true/) }
    /"prerelease"[[:space:]]*:/ {
      pre = ($0 ~ /true/)
      if (tag != "" && draft == 0) {
        if (pre == 0 && stable == "") stable = tag
        if (pre == 1 && pretag == "") pretag = tag
      }
      tag = ""
    }
    END {
      if (stable != "") print stable
      else if (pretag != "") print pretag
      else exit 1
    }
  ') || {
    echo "install.sh: no release was published" >&2
    exit 1
  }
  printf '%s\n' "$picked"
}

if [ -z "$version" ]; then
  version=$(resolve_version)
fi

archive="kite_${version}_${goos}_${goarch}.tar.gz"
base="${release_base}/${version}"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/kite-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

echo "Downloading ${archive}"
if ! curl -fsSL --retry 3 --retry-delay 1 -A kite-install -o "${tmp}/${archive}" "${base}/${archive}"; then
  echo "install.sh: release ${version} was not found" >&2
  exit 1
fi
if ! curl -fsSL --retry 3 --retry-delay 1 -A kite-install -o "${tmp}/SHA256SUMS" "${base}/SHA256SUMS"; then
  echo "install.sh: release ${version} was not found (no SHA256SUMS)" >&2
  exit 1
fi

want=$(awk -v f="$archive" '
  $1 ~ /^[0-9A-Fa-f][0-9A-Fa-f]*$/ && length($1) == 64 {
    name = $2
    sub(/^\*/, "", name)
    if (name == f) { print tolower($1); exit }
  }
' "${tmp}/SHA256SUMS")
if [ -z "$want" ]; then
  echo "install.sh: SHA256SUMS has no entry for ${archive}" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "${tmp}/${archive}" | awk '{print tolower($1)}')
elif command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "${tmp}/${archive}" | awk '{print tolower($1)}')
else
  echo "install.sh: sha256sum or shasum is required to verify the download" >&2
  exit 1
fi
if [ "$got" != "$want" ]; then
  echo "install.sh: checksum mismatch for ${archive}" >&2
  exit 1
fi

tar -xzf "${tmp}/${archive}" -C "$tmp"

bindir=""
use_sudo=0
# KITE_BIN_DIR forces the destination and skips sudo. The normal path is
# /usr/local/bin, or ~/.local/bin when that is not writable.
if [ -n "${KITE_BIN_DIR:-}" ]; then
  bindir=$KITE_BIN_DIR
  mkdir -p "$bindir"
elif [ "$(id -u)" -eq 0 ]; then
  bindir=/usr/local/bin
elif [ -w /usr/local/bin ] || { [ ! -e /usr/local/bin ] && [ -w /usr/local ]; }; then
  bindir=/usr/local/bin
elif command -v sudo >/dev/null 2>&1; then
  if sudo mkdir -p /usr/local/bin; then
    bindir=/usr/local/bin
    use_sudo=1
  fi
fi
if [ -z "$bindir" ]; then
  bindir="${HOME}/.local/bin"
  use_sudo=0
  mkdir -p "$bindir"
  echo "No permission to write /usr/local/bin. Installing to ${bindir}."
fi
if [ "$use_sudo" -eq 0 ]; then
  mkdir -p "$bindir"
fi

copy_bin() {
  src=$1
  name=$2
  dest="${bindir}/${name}"
  if command -v install >/dev/null 2>&1; then
    if [ "$use_sudo" -eq 1 ]; then
      sudo install -m 755 "$src" "$dest"
    else
      install -m 755 "$src" "$dest"
    fi
  else
    if [ "$use_sudo" -eq 1 ]; then
      sudo cp "$src" "$dest"
      sudo chmod 755 "$dest"
    else
      cp "$src" "$dest"
      chmod 755 "$dest"
    fi
  fi
  if [ "$goos" = "darwin" ]; then
    xattr -d com.apple.quarantine "$dest" 2>/dev/null || true
  fi
}

# instance_exists does not read /usr/local/lib/kite. That directory is 0700,
# so a non-root check of the named binary always fails and used to skip the
# upgrade guard. Unit files, the 0711 state directory, and systemctl/launchctl
# are visible without that directory.
instance_exists() {
  name=$1
  if [ -z "$name" ]; then
    name=kite-agent
  fi
  if [ -f "/etc/systemd/system/${name}.service" ]; then
    return 0
  fi
  if [ -f "/Library/LaunchDaemons/com.kite.${name}.plist" ]; then
    return 0
  fi
  if [ "$name" = "kite-agent" ]; then
    if [ -f /var/lib/kite-agent/agent-control.json ]; then
      return 0
    fi
  elif [ -d "/var/lib/kite-agent/${name}" ]; then
    return 0
  fi
  if command -v systemctl >/dev/null 2>&1; then
    if systemctl cat "${name}.service" >/dev/null 2>&1; then
      return 0
    fi
  fi
  if command -v launchctl >/dev/null 2>&1; then
    if launchctl print "system/com.kite.${name}" >/dev/null 2>&1; then
      return 0
    fi
  fi
  if [ "$name" = "kite-agent" ]; then
    if [ -x "${bindir}/kite-agent" ] || [ -x /usr/local/bin/kite-agent ]; then
      return 0
    fi
    if command -v kite-agent >/dev/null 2>&1; then
      return 0
    fi
  fi
  return 1
}

instance_flag() {
  if [ -n "$service_name" ] && [ "$service_name" != "kite-agent" ]; then
    printf '%s\n' "--service-name ${service_name}"
  fi
}

note_agent_left_alone() {
  label=kite-agent
  if [ -n "$service_name" ] && [ "$service_name" != "kite-agent" ]; then
    label=$service_name
    if ! instance_exists "$label"; then
      return 0
    fi
    echo "kite-agent service ${label} was not updated. The agent service was not restarted."
    echo "To update it: sudo kite-agent upgrade --service-name ${label}"
    return 0
  fi
  if ! instance_exists kite-agent; then
    return 0
  fi
  agent_bin=""
  if [ -x "${bindir}/kite-agent" ]; then
    agent_bin="${bindir}/kite-agent"
  elif [ -x /usr/local/bin/kite-agent ]; then
    agent_bin=/usr/local/bin/kite-agent
  elif command -v kite-agent >/dev/null 2>&1; then
    agent_bin=$(command -v kite-agent)
  fi
  current=""
  if [ -n "$agent_bin" ]; then
    current=$("$agent_bin" -version 2>/dev/null || true)
    case "$current" in
      *"${version}"*) return 0 ;;
    esac
  fi
  echo "kite-agent (${current:-unknown}) was not updated. The agent service was not restarted."
  echo "To update it: sudo kite-agent upgrade"
}

run_as_root() {
  if [ "$(id -u)" -eq 0 ] || [ "${KITE_NO_SUDO:-}" = "1" ]; then
    "$@"
    return $?
  fi
  if command -v sudo >/dev/null 2>&1; then
    sudo "$@"
    return $?
  fi
  echo "install.sh: need root to run: $*" >&2
  return 1
}

copy_bin "${tmp}/kite" kite
if [ "$relay_server" -eq 1 ]; then
  copy_bin "${tmp}/kite-relay" kite-relay
fi

# A client install updates kite only. It does not replace kite-agent and
# does not restart an agent service.
if [ "$agent" -eq 0 ]; then
  echo "Installed kite ${version} to ${bindir} (kite only; the agent service was not changed)."
  note_agent_left_alone
  case ":${PATH}:" in
    *":${bindir}:"*) ;;
    *)
      echo "Add ${bindir} to PATH:"
      echo "  export PATH=\"${bindir}:\$PATH\""
      ;;
  esac
  exit 0
fi

extra=$(instance_flag)
named=0
svc=kite-agent
if [ -n "$service_name" ] && [ "$service_name" != "kite-agent" ]; then
  named=1
  svc=$service_name
fi
pair_hint="kite-agent pair"
upgrade_hint="sudo kite-agent upgrade"
if [ "$named" -eq 1 ]; then
  pair_hint="kite-agent pair --service-name ${svc}"
  upgrade_hint="sudo kite-agent upgrade --service-name ${svc}"
fi

# The client kite is the only binary this script writes onto PATH.
# A named agent is installed by `kite-agent install --service-name`, which
# copies itself to /usr/local/lib/kite/<name>/. Never write that binary to
# ${bindir}/kite-agent, the default instance.
if instance_exists "$svc"; then
  runner="${tmp}/kite-agent"
  if [ "$named" -eq 1 ] && [ -x "/usr/local/lib/kite/${svc}/kite-agent" ]; then
    runner="/usr/local/lib/kite/${svc}/kite-agent"
  elif [ "$named" -eq 0 ] && [ -x "${bindir}/kite-agent" ]; then
    runner="${bindir}/kite-agent"
  elif [ "$named" -eq 0 ] && [ -x /usr/local/bin/kite-agent ]; then
    runner=/usr/local/bin/kite-agent
  elif [ -x "${bindir}/kite-agent" ]; then
    runner="${bindir}/kite-agent"
  elif [ -x /usr/local/bin/kite-agent ]; then
    runner=/usr/local/bin/kite-agent
  fi
  # shellcheck disable=SC2086
  if ! run_as_root "$runner" upgrade --from "${tmp}/kite-agent" --version "$version" $extra; then
    echo "install.sh: kite-agent upgrade failed. The agent was not replaced." >&2
    exit 1
  fi
  if [ -z "$code" ]; then
    echo "Upgraded kite-agent ${svc} to ${version}. No install code was given, so the existing token was kept."
    echo "Next, on this machine: ${pair_hint}"
    echo "To update it later: ${upgrade_hint}"
    exit 0
  fi
else
  if [ -z "$relay" ] || [ -z "$code" ]; then
    echo "install.sh: --agent requires --relay and --code when ${svc} is not installed yet" >&2
    exit 1
  fi
  if [ "$named" -eq 0 ]; then
    copy_bin "${tmp}/kite-agent" kite-agent
    runner="${bindir}/kite-agent"
  else
    runner="${tmp}/kite-agent"
  fi
fi

echo "Installed kite ${version} to ${bindir}"
if [ "$named" -eq 1 ]; then
  echo "  kite -> ${bindir}/kite"
  echo "  service ${svc} -> /usr/local/lib/kite/${svc}/kite-agent"
else
  echo "  kite -> ${bindir}/kite"
  echo "  kite-agent -> ${bindir}/kite-agent"
fi
case ":${PATH}:" in
  *":${bindir}:"*) ;;
  *)
    echo "Add ${bindir} to PATH:"
    echo "  export PATH=\"${bindir}:\$PATH\""
    ;;
esac

# shellcheck disable=SC2086
if ! run_as_root "$runner" install --relay "$relay" --code "$code" $extra; then
  echo "install.sh: kite-agent install failed." >&2
  exit 1
fi
echo "Next, on this machine: ${pair_hint}"
echo "If that cannot read the control file: sudo ${pair_hint}"
