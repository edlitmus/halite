#!/bin/sh
# usage: run.sh VERSION  (from the directory holding srv/)
set +e
v=$1; here=$PWD
curl -fsSL -o s.tar.xz https://packages.broadcom.com/artifactory/saltproject-generic/onedir/$v/salt-$v-onedir-linux-x86_64.tar.xz || exit 1
tar xJf s.tar.xz
S=$here/salt; R=$here/srv; mkdir -p cfg
printf 'file_client: local\nfile_roots: {base: [%s]}\nid: cap\n' $R > cfg/minion
echo "===== VERSION $($S/salt-call --version)"
for f in $R/[isx]_*.sls; do
  c=$(basename $f .sls)
  echo "===== CASE $c"
  sudo $S/salt-call --local -c cfg --out=json state.apply $c > out.json 2> err.txt
  echo "EXIT $?"
  echo "JSON $(python3 -c 'import json,sys; print(json.dumps(json.load(open("out.json"))))' 2>/dev/null || echo '"UNPARSED"')"
  grep -v '^\s*$' err.txt | tail -5 | sed 's/^/STDERR /'
done
echo "===== END"
