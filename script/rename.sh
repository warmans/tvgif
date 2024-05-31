set -oe pipefail

if [ -z ${1+x} ]; then echo "1st argument should be the video file extension e.g. .avi"; exit 1; fi
if [ -z ${2+x} ]; then echo "2nd argument should be the publication e.g. partridge"; exit 1; fi

echo "Renaming .${1} files...";
for ep in *.${1}; do \
	echo ${ep} | ~/gomod/github.com/warmans/tvgif/bin/tvgif tools fix-name && \
       	mv "${ep}" "${2}-$(echo ${ep} | ~/gomod/github.com/warmans/tvgif/bin/tvgif tools fix-name).${1}"; \
done;
