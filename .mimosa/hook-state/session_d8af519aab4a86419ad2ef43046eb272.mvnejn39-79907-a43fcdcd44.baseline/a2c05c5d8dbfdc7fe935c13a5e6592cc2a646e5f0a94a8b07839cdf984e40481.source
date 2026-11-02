#!/bin/sh
# 单容器自含起动:MariaDB 后台起 + 幂等建库建用户,Apache 前台(PID 1)。
# 靶场用途:容器停即整站停,数据在容器内(重置 = 删容器重建)。
set -e

mkdir -p /var/run/mysqld
chown mysql:mysql /var/run/mysqld
mysqld_safe --skip-syslog >/dev/null 2>&1 &

# 等 MariaDB 就绪(最长 60 秒)
i=0
until mysqladmin ping >/dev/null 2>&1; do
    i=$((i + 1))
    [ "$i" -ge 60 ] && { echo "entrypoint: MariaDB 60 秒未就绪" >&2; exit 1; }
    sleep 1
done

# 幂等建库建用户(DVWA setup.php 只建表,库与用户须先就位)
mysql -e "CREATE DATABASE IF NOT EXISTS dvwa; \
CREATE USER IF NOT EXISTS 'dvwa'@'127.0.0.1' IDENTIFIED BY 'p@ssw0rd'; \
CREATE USER IF NOT EXISTS 'dvwa'@'localhost' IDENTIFIED BY 'p@ssw0rd'; \
GRANT ALL ON dvwa.* TO 'dvwa'@'127.0.0.1'; \
GRANT ALL ON dvwa.* TO 'dvwa'@'localhost'; \
FLUSH PRIVILEGES;"

exec apache2-foreground
