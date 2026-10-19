# GhostReaper 本地靶场 · DVWA 主力镜像(单容器自含 Apache + MariaDB,
# 一个容器一个网站)。源码取官方 digininja/DVWA,commit 钉死保证可复现
# (升级 = 换 DVWA_SHA 重建)。基底与 PHP 扩展对齐官方 Dockerfile。
FROM php:8-apache

ARG DVWA_SHA=068d974487a297c155c48722f9fcf7180a83adc4
LABEL ghostreaper.lab="dvwa" \
      ghostreaper.dvwa.sha="${DVWA_SHA}" \
      org.opencontainers.image.source="https://github.com/digininja/DVWA" \
      org.opencontainers.image.licenses="gpl-3.0"

COPY --from=composer:2 /usr/bin/composer /usr/local/bin/composer

RUN apt-get update \
 && export DEBIAN_FRONTEND=noninteractive \
 && apt-get install -y --no-install-recommends \
      zlib1g-dev libpng-dev libjpeg-dev libfreetype6-dev iputils-ping zip unzip \
      default-mysql-server \
 && apt-get clean -y && rm -rf /var/lib/apt/lists/* \
 && docker-php-ext-configure gd --with-jpeg --with-freetype \
 && a2enmod rewrite \
 && docker-php-ext-install gd mysqli pdo pdo_mysql \
 && echo "ServerName localhost" >> /etc/apache2/apache2.conf

# DVWA 源码(tarball 下载,不依赖 git)。远程 URL 的 ADD 不会自动解包。
ADD https://github.com/digininja/DVWA/archive/${DVWA_SHA}.tar.gz /tmp/dvwa-src.tar.gz
RUN tar -xzf /tmp/dvwa-src.tar.gz -C /tmp \
 && mv /tmp/DVWA-${DVWA_SHA}/* /var/www/html/ \
 && rm -rf /tmp/dvwa-src.tar.gz /tmp/DVWA-${DVWA_SHA} \
 # API 模块依赖(与官方 Dockerfile 同步骤;失败会如实暴露在 build 日志)
 && cd /var/www/html/vulnerabilities/api && composer install --no-interaction --no-progress \
 && cd /var/www/html && chown -R www-data:www-data /var/www/html

# 站点配置:库指向本容器 MariaDB;默认安全等级 low(登录后仍可按
# low/medium/high/impossible 逐会话调整);文件包含模块需 allow_url_include。
COPY config.inc.php /var/www/html/config/config.inc.php
COPY dvwa.ini /usr/local/etc/php/conf.d/dvwa.ini
COPY entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh && chown www-data:www-data /var/www/html/config/config.inc.php

EXPOSE 80
ENTRYPOINT ["/entrypoint.sh"]
