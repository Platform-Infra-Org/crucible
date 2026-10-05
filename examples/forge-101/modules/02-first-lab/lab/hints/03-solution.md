Run in the **web** terminal:

    sed -i 's/8081;/80;/g' /etc/nginx/conf.d/default.conf && nginx -s reload
