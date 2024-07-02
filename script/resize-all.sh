set -oe pipefail

if [ -z ${1+x} ]; then echo "1st argument should be the video file extension e.g. .avi"; exit 1; fi

echo "Processing .${1} files... ";
for ep in *.${1}; do \
	./tools/resize-one.sh ${ep}; \
done;
