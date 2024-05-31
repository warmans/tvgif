set -o pipefail

if [ -z ${1+x} ]; then echo "Argument should be the video file extension e.g. .avi"; exit 1; fi

echo "Extracting subs from .${1} files...";

for ep in *.${1}; do ffmpeg -i "${ep}" "$(basename ${ep} .${1}).srt"; done;
