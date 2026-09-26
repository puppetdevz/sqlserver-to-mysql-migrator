#!/bin/bash
#
# lib.sh - 公共 Shell 函数库
# 被 backup.sh、restore.sh 和其他 scripts 引用
#
set -euo pipefail

# ========== 脚本目录 ==========
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ========== 备份文件命名常量 ==========
BACKUP_PREFIX="target_database_"
TIMESTAMP_FORMAT="%Y%m%d%H%M"
TIMESTAMP_PATTERN='[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]'

# ========== 数据库配置 ==========
DB_HOST="${DB_HOST:-127.0.0.1}"
DB_PORT="${DB_PORT:-3306}"
DB_NAME="${DB_NAME:-migration_example_mysql}"
DB_USER="${DB_USER:-root}"
DB_PASS="${DB_PASS:-}"  # 必须通过环境变量设置

# ========== 备份目录 ==========
BACKUP_DIR="${BACKUP_DIR:-$SCRIPT_DIR}"

# ========== 颜色输出 ==========
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo_step() { echo -e "${GREEN}[${1}]${NC} $2"; }
echo_info() { echo -e "${BLUE}[Info]${NC} $1"; }
echo_warn() { echo -e "${YELLOW}[Warn]${NC} $1"; }
echo_error() { echo -e "${RED}[Error]${NC} $1" >&2; }

# ========== 错误处理 ==========
die() {
    echo_error "$1"
    exit 1
}

# ========== 检查命令是否存在 ==========
check_cmd() {
    local cmd="$1"
    local msg="${2:-请确保已安装并加入 PATH}"
    command -v "$cmd" &> /dev/null || die "未找到 $cmd 命令，$msg"
}

# ========== 初始化备份目录 ==========
ensure_backup_dir() {
    if [[ -f "$BACKUP_DIR" ]]; then
        die "备份目录是文件而非目录: $BACKUP_DIR"
    fi
    mkdir -p "$BACKUP_DIR" || die "无法创建备份目录: $BACKUP_DIR"
    BACKUP_DIR="$(cd "$BACKUP_DIR" && pwd)" || die "无法进入备份目录: $BACKUP_DIR"
}

# ========== 生成时间戳 ==========
generate_timestamp() {
    date +"$TIMESTAMP_FORMAT"
}

# ========== 计算耗时 ==========
calc_duration() {
    local start="$1"
    echo $(($(date +%s) - start))
}

# ========== 查找最新备份 ==========
find_latest_backup() {
    local latest_file=""
    local latest_ts=""

    # 启用 nullglob，glob 无匹配时循环不执行
    shopt -s nullglob
    for f in "$BACKUP_DIR"/${BACKUP_PREFIX}${TIMESTAMP_PATTERN}.sql; do
        local ts="${f##*/}"  # basename
        ts="${ts#"$BACKUP_PREFIX"}"
        ts="${ts%.sql}"

        if [[ -z "$latest_ts" ]] || (( 10#$ts > 10#$latest_ts )); then
            latest_ts="$ts"
            latest_file="$f"
        fi
    done
    shopt -u nullglob

    if [[ -n "$latest_file" ]]; then
        echo "$latest_file"
    fi
    return 0
}

# ========== 备份文件路径 ==========
backup_file_path() {
    local ts="$1"
    echo "$BACKUP_DIR/${BACKUP_PREFIX}${ts}.sql"
}

# ========== 备份文件完整性 ==========
sha256_file() {
    local file="$1"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum -- "$file" | cut -d ' ' -f 1
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 -- "$file" | cut -d ' ' -f 1
    else
        echo_error "需要 sha256sum 或 shasum 来验证备份完整性"
        return 1
    fi
}

# ========== 执行 mysqldump ==========
# 用法: run_mysqldump <output_file>
run_mysqldump() {
    local output="$1"

    export MYSQL_PWD="$DB_PASS"
    mysqldump --host="$DB_HOST" \
              --port="$DB_PORT" \
              --user="$DB_USER" \
              --default-character-set=utf8mb4 \
              --single-transaction \
              --quick \
              --no-tablespaces \
              --set-gtid-purged=OFF \
              --add-drop-table \
              --routines \
              --events \
              --triggers \
              --hex-blob \
              "$DB_NAME" > "$output"
    local status=$?
    unset MYSQL_PWD

    return $status
}

# ========== 执行 mysql restore ==========
# 用法: run_mysql_restore <input_file>
run_mysql_restore() {
    local input="$1"

    export MYSQL_PWD="$DB_PASS"
    { mysql --host="$DB_HOST" \
            --port="$DB_PORT" \
            --user="$DB_USER" \
            --database="$DB_NAME" \
            --default-character-set=utf8mb4; } < "$input" 2>&1
    local status=$?
    unset MYSQL_PWD

    return $status
}

# ========== 获取数据库所有表名 ==========
# 用法: get_all_tables
# 输出: 每行一个表名
get_all_tables() {
    export MYSQL_PWD="$DB_PASS"
    mysql --host="$DB_HOST" \
          --port="$DB_PORT" \
          --user="$DB_USER" \
          --database="$DB_NAME" \
          --skip-column-names \
          --batch \
          -e "SHOW TABLES;"
    local status=$?
    unset MYSQL_PWD
    return $status
}

# ========== 获取数据库对象 ==========
# 用法: get_schema_objects
# 输出: 每行一个对象，格式为 TYPE<TAB>NAME
get_schema_objects() {
    export MYSQL_PWD="$DB_PASS"
    mysql --host="$DB_HOST" \
          --port="$DB_PORT" \
          --user="$DB_USER" \
          --database="$DB_NAME" \
          --skip-column-names \
          --batch \
          --raw \
          -e "
SELECT object_type, object_name
FROM (
    SELECT 'VIEW' AS object_type, TABLE_NAME AS object_name
      FROM information_schema.TABLES
     WHERE TABLE_SCHEMA = DATABASE()
       AND TABLE_TYPE = 'VIEW'
    UNION ALL
    SELECT 'TABLE' AS object_type, TABLE_NAME AS object_name
      FROM information_schema.TABLES
     WHERE TABLE_SCHEMA = DATABASE()
       AND TABLE_TYPE = 'BASE TABLE'
    UNION ALL
    SELECT ROUTINE_TYPE AS object_type, ROUTINE_NAME AS object_name
      FROM information_schema.ROUTINES
     WHERE ROUTINE_SCHEMA = DATABASE()
    UNION ALL
    SELECT 'EVENT' AS object_type, EVENT_NAME AS object_name
      FROM information_schema.EVENTS
     WHERE EVENT_SCHEMA = DATABASE()
) AS schema_objects
ORDER BY CASE object_type
    WHEN 'VIEW' THEN 1
    WHEN 'TABLE' THEN 2
    WHEN 'PROCEDURE' THEN 3
    WHEN 'FUNCTION' THEN 4
    WHEN 'EVENT' THEN 5
    ELSE 6
END, object_name;"
    local status=$?
    unset MYSQL_PWD
    return $status
}

# ========== MySQL 标识符转义 ==========
quote_mysql_identifier() {
    local identifier="$1"
    identifier="${identifier//\`/\`\`}"
    printf '`%s`' "$identifier"
}

# ========== 生成清空库内对象的 SQL ==========
# 用法: generate_drop_statements [objects]
# 输出: DROP VIEW/TABLE/PROCEDURE/FUNCTION/EVENT 语句
generate_drop_statements() {
    local objects="${1:-}"
    if [[ $# -eq 0 ]]; then
        objects=$(get_schema_objects) || return 1
    fi

    printf 'SET FOREIGN_KEY_CHECKS = 0;\n'
    while IFS=$'\t' read -r object_type object_name; do
        [[ -z "${object_type:-}" || -z "${object_name:-}" ]] && continue

        case "$object_type" in
            VIEW)
                printf 'DROP VIEW IF EXISTS %s;\n' "$(quote_mysql_identifier "$object_name")"
                ;;
            TABLE)
                printf 'DROP TABLE IF EXISTS %s;\n' "$(quote_mysql_identifier "$object_name")"
                ;;
            PROCEDURE)
                printf 'DROP PROCEDURE IF EXISTS %s;\n' "$(quote_mysql_identifier "$object_name")"
                ;;
            FUNCTION)
                printf 'DROP FUNCTION IF EXISTS %s;\n' "$(quote_mysql_identifier "$object_name")"
                ;;
            EVENT)
                printf 'DROP EVENT IF EXISTS %s;\n' "$(quote_mysql_identifier "$object_name")"
                ;;
        esac
    done <<< "$objects"
    printf 'SET FOREIGN_KEY_CHECKS = 1;\n'
}
