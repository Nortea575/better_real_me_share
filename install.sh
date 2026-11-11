#!/bin/sh
set -eu

INSTALL_DIR=/opt/real_me_add
DATA_DIR=$INSTALL_DIR/data
UNIT_NAME=real-me-add.service
UNIT_PATH=/etc/systemd/system/real-me-add.service
SERVICE_FILE=$INSTALL_DIR/$UNIT_NAME
MARKER_FILE=$INSTALL_DIR/.real-me-add-managed
MARKER_VALUE=real-me-add-managed-v1
PUBLIC_URL=${REAL_ME_PUBLIC_URL:-}
LISTEN=${REAL_ME_LISTEN:-0.0.0.0:18766}
TLS_CERT=${REAL_ME_TLS_CERT:-}
TLS_KEY=${REAL_ME_TLS_KEY:-}
FIRST_INSTALL=0
BIN_NEW=$INSTALL_DIR/.brm-share.new
UNIT_NEW=$INSTALL_DIR/.real-me-add.service.new

fail() {
  printf '%s\n' "real-me-share installer: $*" >&2
  exit 1
}

if [ "$(id -u)" -ne 0 ]; then
  fail '请使用 root 运行安装器。'
fi
for command_name in uname install mv systemctl readlink sed cat chmod grep; do
  command -v "$command_name" >/dev/null 2>&1 || fail "缺少系统命令：$command_name"
done
[ -n "$PUBLIC_URL" ] || fail '请显式设置 REAL_ME_PUBLIC_URL。'

case "$(uname -m)" in
  x86_64|amd64) ARCH=linux-amd64 ;;
  aarch64|arm64) ARCH=linux-arm64 ;;
  *) fail "不支持此 CPU 架构：$(uname -m)" ;;
esac

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
SOURCE_BINARY=$SCRIPT_DIR/$ARCH/brm-share
[ -f "$SOURCE_BINARY" ] || fail "安装包缺少 $ARCH/brm-share。"
"$SOURCE_BINARY" version >/dev/null || fail '安装包中的服务程序无法启动。'

case "$PUBLIC_URL" in
  http://*|https://*) ;;
  *) fail 'REAL_ME_PUBLIC_URL 必须以 http:// 或 https:// 开头。' ;;
esac
case "$PUBLIC_URL" in
  *[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789:/._~%-]*)
    fail 'REAL_ME_PUBLIC_URL 含安装器不支持的字符；路径中的特殊字符请使用百分号编码。' ;;
esac
case "$PUBLIC_URL" in
  *\?*|*\#*|*@*) fail 'REAL_ME_PUBLIC_URL 不能包含查询、片段或用户凭据。' ;;
esac
URL_AUTHORITY=${PUBLIC_URL#*://}
URL_AUTHORITY=${URL_AUTHORITY%%/*}
[ -n "$URL_AUTHORITY" ] || fail 'REAL_ME_PUBLIC_URL 必须包含主机名。'
case "$URL_AUTHORITY" in
  *[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.:-]*) fail 'REAL_ME_PUBLIC_URL 主机名或端口无效。' ;;
esac
case "$LISTEN" in
  *[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789:._-]*) fail 'REAL_ME_LISTEN 含不支持的字符。' ;;
esac
if [ -n "$TLS_CERT" ] || [ -n "$TLS_KEY" ]; then
  [ -n "$TLS_CERT" ] && [ -n "$TLS_KEY" ] || fail 'REAL_ME_TLS_CERT 与 REAL_ME_TLS_KEY 必须同时设置。'
  case "$TLS_CERT$TLS_KEY" in
    *[!ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_./-]*) fail 'TLS 证书路径只能包含常见路径字符。' ;;
  esac
  [ -r "$TLS_CERT" ] || fail "TLS 证书不可读：$TLS_CERT"
  [ -r "$TLS_KEY" ] || fail "TLS 私钥不可读：$TLS_KEY"
  case "$PUBLIC_URL" in https://*) ;; *) fail '本机 TLS 需要 https:// 公开 URL。' ;; esac
fi

if [ -L "$INSTALL_DIR" ]; then
  fail "$INSTALL_DIR 是符号链接；为避免覆盖其他目录，安装已停止。"
elif [ -e "$INSTALL_DIR" ]; then
  [ -d "$INSTALL_DIR" ] || fail "$INSTALL_DIR 已存在且不是目录。"
  [ -f "$MARKER_FILE" ] || fail "$INSTALL_DIR 未标记为本安装器管理的目录；不会覆盖。"
  [ "$(cat "$MARKER_FILE")" = "$MARKER_VALUE" ] || fail "$INSTALL_DIR 管理标记不匹配；不会覆盖。"
else
  FIRST_INSTALL=1
fi

existing_fragment=$(systemctl show --property=FragmentPath --value "$UNIT_NAME" 2>/dev/null || true)
if [ -n "$existing_fragment" ] && [ "$existing_fragment" != "$SERVICE_FILE" ]; then
  fail "系统中已有同名单元（$existing_fragment）；不会覆盖。"
fi
if [ -e "$UNIT_PATH" ] || [ -L "$UNIT_PATH" ]; then
  [ -L "$UNIT_PATH" ] || fail "$UNIT_PATH 已存在且不是本服务链接；不会覆盖。"
  current_target=$(readlink -f "$UNIT_PATH" 2>/dev/null || true)
  [ "$current_target" = "$SERVICE_FILE" ] || fail "$UNIT_PATH 指向其他目标；不会覆盖。"
fi

if ! { [ "$FIRST_INSTALL" -eq 0 ] && systemctl is-active --quiet "$UNIT_NAME" && grep -Fxq "Environment=REAL_ME_LISTEN=$LISTEN" "$SERVICE_FILE"; }; then
  "$SOURCE_BINARY" check-listen --listen "$LISTEN" || fail "监听地址 $LISTEN 不可用；没有写入安装文件。"
fi

if [ "$FIRST_INSTALL" -eq 1 ]; then
  install -d -m 0755 "$INSTALL_DIR"
  printf '%s\n' "$MARKER_VALUE" > "$MARKER_FILE"
  chmod 0644 "$MARKER_FILE"
fi

trap 'rm -f "$BIN_NEW" "$UNIT_NEW"' EXIT HUP INT TERM
install -m 0755 "$SOURCE_BINARY" "$BIN_NEW"
mv -f "$BIN_NEW" "$INSTALL_DIR/brm-share"

URL_FOR_SYSTEMD=$(printf '%s' "$PUBLIC_URL" | sed 's/%/%%/g')
CERT_FOR_SYSTEMD=$(printf '%s' "$TLS_CERT" | sed 's/%/%%/g')
KEY_FOR_SYSTEMD=$(printf '%s' "$TLS_KEY" | sed 's/%/%%/g')
cat > "$UNIT_NEW" <<EOF
[Unit]
Description=Better Real Me public sharing service
After=network-online.target
Wants=network-online.target
# Managed by the real-me-share installer. Do not edit elsewhere.

[Service]
Type=simple
WorkingDirectory=/opt/real_me_add
Environment=REAL_ME_DATA_DIR=/opt/real_me_add/data
Environment=REAL_ME_LISTEN=$LISTEN
Environment="REAL_ME_PUBLIC_URL=$URL_FOR_SYSTEMD"
Environment="REAL_ME_TLS_CERT=$CERT_FOR_SYSTEMD"
Environment="REAL_ME_TLS_KEY=$KEY_FOR_SYSTEMD"
ExecStart=/opt/real_me_add/brm-share serve
Restart=on-failure
RestartSec=3
TimeoutStopSec=20
UMask=0077
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
LockPersonality=yes
RestrictSUIDSGID=yes
MemoryDenyWriteExecute=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
ReadWritePaths=/opt/real_me_add/data

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "$UNIT_NEW"
mv -f "$UNIT_NEW" "$SERVICE_FILE"

if [ "$FIRST_INSTALL" -eq 1 ]; then
  printf '%s\n' '首次管理员设置链接（仅显示一次，请妥善保存）：'
  "$INSTALL_DIR/brm-share" setup-link --data-dir "$DATA_DIR" --public-url "$PUBLIC_URL"
fi

if [ ! -e "$UNIT_PATH" ] && [ ! -L "$UNIT_PATH" ]; then
  systemctl link "$SERVICE_FILE"
fi
systemctl daemon-reload
if [ "$FIRST_INSTALL" -eq 1 ]; then
  systemctl enable --now "$UNIT_NAME"
else
  systemctl enable "$UNIT_NAME"
  systemctl restart "$UNIT_NAME"
fi
printf '%s\n' "安装完成：$UNIT_NAME 已启动，监听 $LISTEN；数据保存在 $DATA_DIR。"
