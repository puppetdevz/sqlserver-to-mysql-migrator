#!/bin/bash
#
# backup.sh
# 备份目标 MySQL 数据库完整状态
#
# 用法:
#   ./backup.sh                  # 使用 scripts 目录作为备份目录
#   BACKUP_DIR=/path/to/dir ./backup.sh # 通过环境变量指定备份目录
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

# ========== 参数解析 ==========
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --help|-h)
                cat << EOF
用法: ./backup.sh

  备份文件将保存在 scripts/ 目录下

示例:
  cd scripts && ./backup.sh          # 进入 scripts 目录执行
  ./scripts/backup.sh                 # 从项目根目录执行
  BACKUP_DIR=/path/to/dir ./backup.sh # 通过环境变量指定备份目录
EOF
                exit 0
                ;;
            *)
                shift
                ;;
        esac
    done
}

# ========== 前置检查 ==========
check_prerequisites() {
    if ! command -v mysqldump &> /dev/null; then
        die "未找到 mysqldump 命令，请确保 MySQL 客户端已安装"
    fi
    ensure_backup_dir
}

# ========== 备份函数 ==========
do_backup() {
    local ts
    ts=$(generate_timestamp)
    local backup_file
    backup_file=$(backup_file_path "$ts")

    if [[ -f "$backup_file" ]]; then
        die "备份文件已存在: $backup_file"
    fi

    echo_step "Backup" "开始备份数据库 $DB_NAME 到 $backup_file"
    local start_time=$(date +%s)

    # 使用环境变量传递密码，避免命令行暴露
    set +e
    export MYSQL_PWD="$DB_PASS"
    { mysqldump --host="$DB_HOST" \
                --port="$DB_PORT" \
                --user="$DB_USER" \
                --single-transaction \
                --quick \
                --no-tablespaces \
                --set-gtid-purged=OFF \
                --databases "$DB_NAME"; } > "$backup_file" 2>&1
    local dump_status=$?
    unset MYSQL_PWD
    set -e

    if [[ $dump_status -ne 0 ]]; then
        rm -f "$backup_file"
        die "备份失败，请检查数据库连接和权限"
    fi

    local duration=$(($(date +%s) - start_time))
    local size=$(du -h "$backup_file" | cut -f1)

    echo_step "Backup" "备份完成，耗时 ${duration}s，文件大小 $size"
    echo_info "备份文件: $backup_file"
}

# ========== 主流程 ==========
main() {
    parse_args "$@"
    check_prerequisites
    do_backup
}

main "$@"