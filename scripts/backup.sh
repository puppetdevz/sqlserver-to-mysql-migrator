#!/bin/bash
# 数据库备份脚本
# 用法: ./backup.sh [--config /path/to/config.yaml]

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="${SCRIPT_DIR}/config.yaml"

# 解析命令行参数
while [[ $# -gt 0 ]]; do
    case "$1" in
        --config)
            CONFIG_FILE="$2"
            shift 2
            ;;
        --help|-h)
            echo "用法: $0 [--config /path/to/config.yaml]"
            echo ""
            echo "参数:"
            echo "  --config /path/to/config.yaml  指定配置文件路径（默认: scripts/config.yaml）"
            echo "  --help, -h                     显示此帮助信息"
            exit 0
            ;;
        *)
            echo -e "\033[31m错误: 未知参数 '$1'\033[0m"
            echo "使用 --help 查看帮助信息"
            exit 1
            ;;
    esac
done

# 颜色定义
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# 打印带颜色的消息
print_step() { echo -e "${GREEN}[步骤]${NC} $1"; }
print_info() { echo -e "${BLUE}[信息]${NC} $1"; }
print_error() { echo -e "${RED}[错误]${NC} $1"; }

# 检查 mysqldump 命令
print_step "检查 mysqldump 命令..."
if ! command -v mysqldump &> /dev/null; then
    print_error "mysqldump 命令未找到，请安装 MySQL 客户端"
    exit 1
fi
print_info "mysqldump 已安装"

# 检查配置文件
print_step "检查配置文件..."
if [[ ! -f "$CONFIG_FILE" ]]; then
    print_error "配置文件不存在: $CONFIG_FILE"
    exit 1
fi
print_info "配置文件: $CONFIG_FILE"

# 使用 awk 解析嵌套 YAML 配置
print_step "解析配置文件..."

DB_HOST=$(awk '/^target:/ {found=1; next} found && /^  host:/ {gsub(/^  host: */, ""); gsub(/"/, ""); print; exit}' "$CONFIG_FILE")
DB_PORT=$(awk '/^target:/ {found=1; next} found && /^  port:/ {gsub(/^  port: */, ""); print; exit}' "$CONFIG_FILE")
DB_NAME=$(awk '/^target:/ {found=1; next} found && /^  database:/ {gsub(/^  database: */, ""); gsub(/"/, ""); print; exit}' "$CONFIG_FILE")
DB_USER=$(awk '/^target:/ {found=1; next} found && /^  user:/ {gsub(/^  user: */, ""); gsub(/"/, ""); print; exit}' "$CONFIG_FILE")
DB_PASS=$(awk '/^target:/ {found=1; next} found && /^  password:/ {gsub(/^  password: */, ""); gsub(/"/, ""); print; exit}' "$CONFIG_FILE")

# 验证必需字段
print_step "验证配置字段..."
MISSING_FIELDS=""

if [[ -z "$DB_HOST" ]]; then
    MISSING_FIELDS="${MISSING_FIELDS}host "
fi
if [[ -z "$DB_PORT" ]]; then
    MISSING_FIELDS="${MISSING_FIELDS}port "
fi
if [[ -z "$DB_NAME" ]]; then
    MISSING_FIELDS="${MISSING_FIELDS}database "
fi
if [[ -z "$DB_USER" ]]; then
    MISSING_FIELDS="${MISSING_FIELDS}user "
fi
if [[ -z "$DB_PASS" ]]; then
    MISSING_FIELDS="${MISSING_FIELDS}password "
fi

if [[ -n "$MISSING_FIELDS" ]]; then
    print_error "配置缺少必需字段: ${MISSING_FIELDS}"
    exit 1
fi

print_info "数据库: ${DB_NAME}@${DB_HOST}:${DB_PORT}"

# 生成时间戳 (YYYYMMDDHHMM)
print_step "生成备份文件名..."
TIMESTAMP=$(date +%Y%m%d%H%M)
BACKUP_FILE="data/${DB_NAME}_${TIMESTAMP}.sql"

# 检查备份文件是否存在
print_step "检查备份文件..."
if [[ -f "$BACKUP_FILE" ]]; then
    print_error "备份文件已存在，不允许覆盖: $BACKUP_FILE"
    exit 1
fi

# 确保 data 目录存在
mkdir -p data

# 执行备份
print_step "开始备份数据库..."
print_info "输出文件: $BACKUP_FILE"
print_info "使用参数: --single-transaction --quick --set-gtid-purged=OFF"

# 设置密码环境变量（避免密码在命令行中暴露）
export MYSQL_PWD="$DB_PASS"

if ! mysqldump --single-transaction --quick --set-gtid-purged=OFF \
    -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" \
    --databases "$DB_NAME" > "$BACKUP_FILE" 2>&1; then
    print_error "备份失败，正在清理临时文件..."
    rm -f "$BACKUP_FILE"
    unset MYSQL_PWD
    exit 1
fi

unset MYSQL_PWD

# 验证备份文件
if [[ ! -s "$BACKUP_FILE" ]]; then
    print_error "备份文件为空，备份失败"
    rm -f "$BACKUP_FILE"
    exit 1
fi

FILE_SIZE=$(du -h "$BACKUP_FILE" | cut -f1)
print_step "备份完成!"
print_info "文件: $BACKUP_FILE (${FILE_SIZE})"