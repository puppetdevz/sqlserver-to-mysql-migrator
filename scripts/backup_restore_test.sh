#!/bin/bash
#
# Lightweight regression tests for backup.sh / restore.sh.
# Uses fake mysql clients, so it does not touch a real database.
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/migration_example-backup-restore-test.XXXXXX")"
FAKE_BIN="$TEST_ROOT/bin"
mkdir -p "$FAKE_BIN"

failures=0

fail() {
    echo "not ok - $1" >&2
    return 1
}

assert_equal() {
    local expected="$1"
    local actual="$2"
    local message="$3"

    [[ "$expected" == "$actual" ]] || fail "$message: expected '$expected', got '$actual'"
}

assert_file_exists() {
    local path="$1"
    [[ -f "$path" ]] || fail "expected file to exist: $path"
}

assert_contains() {
    local path="$1"
    local needle="$2"

    grep -Fq "$needle" "$path" || fail "expected $path to contain: $needle"
}

assert_not_contains() {
    local path="$1"
    local needle="$2"

    if grep -Fq "$needle" "$path"; then
        fail "expected $path not to contain: $needle"
    fi
}

single_backup_file() {
    local backup_dir="$1"
    local had_nullglob=0

    shopt -q nullglob || had_nullglob=1
    shopt -s nullglob
    local files=("$backup_dir"/target_database_*.sql)
    [[ $had_nullglob -eq 1 ]] && shopt -u nullglob

    [[ ${#files[@]} -eq 1 ]] || fail "expected one backup file in $backup_dir, got ${#files[@]}"
    echo "${files[0]}"
}

create_fake_clients() {
    cat > "$FAKE_BIN/mysql" <<'MYSQL_STUB'
#!/bin/bash
set -uo pipefail

query=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        -e)
            query="$2"
            shift 2
            ;;
        *)
            shift
            ;;
    esac
done

if [[ -n "$query" ]]; then
    if [[ -n "${FAKE_MYSQL_OBJECTS_FILE:-}" && -f "$FAKE_MYSQL_OBJECTS_FILE" ]]; then
        cat "$FAKE_MYSQL_OBJECTS_FILE"
    fi
    exit 0
fi

count_file="${FAKE_MYSQL_COUNT_FILE:?FAKE_MYSQL_COUNT_FILE is required}"
input_prefix="${FAKE_MYSQL_INPUT_PREFIX:?FAKE_MYSQL_INPUT_PREFIX is required}"

count=0
if [[ -f "$count_file" ]]; then
    count="$(cat "$count_file")"
fi
count=$((count + 1))
printf '%s' "$count" > "$count_file"
cat > "${input_prefix}.${count}.sql"
MYSQL_STUB

    cat > "$FAKE_BIN/mysqldump" <<'MYSQLDUMP_STUB'
#!/bin/bash
set -uo pipefail

printf 'CREATE TABLE `sample` (`id` int);\n'
printf 'INSERT INTO `sample` VALUES (1);\n'
if [[ -n "${FAKE_MYSQLDUMP_WARNING:-}" ]]; then
    printf '%s\n' "$FAKE_MYSQLDUMP_WARNING" >&2
fi
MYSQLDUMP_STUB

    chmod +x "$FAKE_BIN/mysql" "$FAKE_BIN/mysqldump"
}

test_backup_writes_clean_sql_file() {
    local backup_dir="$TEST_ROOT/backup-clean"
    local stdout_file="$TEST_ROOT/backup-clean.out"
    local stderr_file="$TEST_ROOT/backup-clean.err"
    mkdir -p "$backup_dir"

    PATH="$FAKE_BIN:$PATH" \
    DB_PASS=secret \
    BACKUP_DIR="$backup_dir" \
    FAKE_MYSQLDUMP_WARNING="Warning: fake mysqldump stderr" \
    bash "$REPO_ROOT/scripts/backup.sh" > "$stdout_file" 2> "$stderr_file"
    local status=$?

    assert_equal "0" "$status" "backup should succeed" || return 1
    local backup_file
    backup_file="$(single_backup_file "$backup_dir")" || return 1
    assert_contains "$backup_file" 'CREATE TABLE `sample`' || return 1
    assert_contains "$backup_file" 'INSERT INTO `sample` VALUES (1);' || return 1
    assert_not_contains "$backup_file" 'Warning: fake mysqldump stderr' || return 1
}

test_restore_imports_backup_into_empty_database() {
    local backup_dir="$TEST_ROOT/restore-empty"
    local objects_file="$TEST_ROOT/restore-empty.objects"
    local count_file="$TEST_ROOT/restore-empty.count"
    local input_prefix="$TEST_ROOT/restore-empty.mysql"
    local stdout_file="$TEST_ROOT/restore-empty.out"
    local stderr_file="$TEST_ROOT/restore-empty.err"
    mkdir -p "$backup_dir"
    : > "$objects_file"
    printf 'CREATE TABLE `sample` (`id` int);\n' > "$backup_dir/target_database_202601010101.sql"

    PATH="$FAKE_BIN:$PATH" \
    DB_PASS=secret \
    BACKUP_DIR="$backup_dir" \
    FAKE_MYSQL_OBJECTS_FILE="$objects_file" \
    FAKE_MYSQL_COUNT_FILE="$count_file" \
    FAKE_MYSQL_INPUT_PREFIX="$input_prefix" \
    bash "$REPO_ROOT/scripts/restore.sh" --timestamp 202601010101 > "$stdout_file" 2> "$stderr_file"
    local status=$?

    assert_equal "0" "$status" "restore should succeed when target database is empty" || return 1
    assert_file_exists "$input_prefix.1.sql" || return 1
    assert_file_exists "$input_prefix.2.sql" || return 1
    assert_contains "$input_prefix.1.sql" 'SET FOREIGN_KEY_CHECKS = 0;' || return 1
    assert_contains "$input_prefix.2.sql" 'CREATE TABLE `sample` (`id` int);' || return 1
}

test_restore_drops_schema_objects_before_importing_backup() {
    local backup_dir="$TEST_ROOT/restore-objects"
    local objects_file="$TEST_ROOT/restore-objects.objects"
    local count_file="$TEST_ROOT/restore-objects.count"
    local input_prefix="$TEST_ROOT/restore-objects.mysql"
    local stdout_file="$TEST_ROOT/restore-objects.out"
    local stderr_file="$TEST_ROOT/restore-objects.err"
    mkdir -p "$backup_dir"
    printf 'VIEW\told_view\nTABLE\told_table\nPROCEDURE\told_proc\nFUNCTION\told_func\nEVENT\told_event\n' > "$objects_file"
    printf 'CREATE TABLE `sample` (`id` int);\n' > "$backup_dir/target_database_202601010102.sql"

    PATH="$FAKE_BIN:$PATH" \
    DB_PASS=secret \
    BACKUP_DIR="$backup_dir" \
    FAKE_MYSQL_OBJECTS_FILE="$objects_file" \
    FAKE_MYSQL_COUNT_FILE="$count_file" \
    FAKE_MYSQL_INPUT_PREFIX="$input_prefix" \
    bash "$REPO_ROOT/scripts/restore.sh" --timestamp 202601010102 > "$stdout_file" 2> "$stderr_file"
    local status=$?

    assert_equal "0" "$status" "restore should succeed after dropping existing schema objects" || return 1
    assert_contains "$input_prefix.1.sql" 'DROP VIEW IF EXISTS `old_view`;' || return 1
    assert_contains "$input_prefix.1.sql" 'DROP TABLE IF EXISTS `old_table`;' || return 1
    assert_contains "$input_prefix.1.sql" 'DROP PROCEDURE IF EXISTS `old_proc`;' || return 1
    assert_contains "$input_prefix.1.sql" 'DROP FUNCTION IF EXISTS `old_func`;' || return 1
    assert_contains "$input_prefix.1.sql" 'DROP EVENT IF EXISTS `old_event`;' || return 1
    assert_contains "$input_prefix.2.sql" 'CREATE TABLE `sample` (`id` int);' || return 1
}

test_restore_reports_missing_timestamp_backup() {
    local backup_dir="$TEST_ROOT/restore-missing"
    local stdout_file="$TEST_ROOT/restore-missing.out"
    local stderr_file="$TEST_ROOT/restore-missing.err"
    mkdir -p "$backup_dir"

    PATH="$FAKE_BIN:$PATH" \
    DB_PASS=secret \
    BACKUP_DIR="$backup_dir" \
    bash "$REPO_ROOT/scripts/restore.sh" --timestamp 202601010103 > "$stdout_file" 2> "$stderr_file"
    local status=$?

    [[ $status -ne 0 ]] || fail "restore should fail when timestamp backup is missing" || return 1
    assert_contains "$stderr_file" '备份文件不存在' || return 1
}

run_test() {
    local name="$1"
    shift

    if "$@"; then
        echo "ok - $name"
    else
        failures=$((failures + 1))
    fi
}

main() {
    create_fake_clients

    run_test "backup writes clean SQL even when mysqldump warns" test_backup_writes_clean_sql_file
    run_test "restore imports backup into empty database" test_restore_imports_backup_into_empty_database
    run_test "restore drops existing schema objects before import" test_restore_drops_schema_objects_before_importing_backup
    run_test "restore reports missing timestamp backup" test_restore_reports_missing_timestamp_backup

    if [[ $failures -gt 0 ]]; then
        echo "$failures test(s) failed" >&2
        exit 1
    fi
}

main "$@"
