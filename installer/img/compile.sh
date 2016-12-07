#!/bin/bash

cp -r installer/app/src    /app
cp -r installer/app/vendor /app

cd /app
/bin/installer-compile

cd /
/go/bin/go-bindata -nomemcopy -nocompress -pkg "installer" app/build/...
