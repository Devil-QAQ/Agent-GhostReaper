<?php

# GhostReaper 靶场定制(基底 = 官方 config.inc.php.dist):
#   - DB 指向本容器内 MariaDB(entrypoint 幂等建库建用户);
#   - 默认安全等级 low(渗透测试靶场默认;登录后可逐会话改);
#   - 验证码模块未配 key(不可用,属如实状态)。

# Database management system to use
$DBMS = 'MySQL';

$_DVWA = array();
$_DVWA[ 'db_server' ]   = '127.0.0.1';
$_DVWA[ 'db_database' ] = 'dvwa';
$_DVWA[ 'db_user' ]     = 'dvwa';
$_DVWA[ 'db_password' ] = 'p@ssw0rd';
$_DVWA[ 'db_port']      = '3306';

# ReCAPTCHA settings(Insecure CAPTCHA 模块;未配置 key 时该模块不可用)
$_DVWA[ 'recaptcha_public_key' ]  = '';
$_DVWA[ 'recaptcha_private_key' ] = '';

# Default security level
$_DVWA[ 'default_security_level' ] = 'low';

# Default locale
$_DVWA[ 'default_locale' ] = 'en';

# Disable authentication(保持登录门:靶场凭据 admin/password 随文档交付)
$_DVWA[ 'disable_authentication' ] = false;

define ('MYSQL', 'mysql');
define ('SQLITE', 'sqlite');

# SQLi DB Backend(SQLi / SQLi Blind 两模块的后端;其余模块不受影响)
$_DVWA['SQLI_DB'] = MYSQL;
