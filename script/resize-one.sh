set -oe pipefail

if [ -z ${1+x} ]; then echo "1st argument should be the video file name"; exit 1; fi

filename=$(basename -- $1);
echo "Converting ${filename} to ${filename%.*}.webm";
#ffmpeg -i $1 -vf "fps=10,scale=596:336:force_original_aspect_ratio=decrease,pad=596:336:-1:-1:color=black" "${filename%.*}.webm";
ffmpeg -i $1 -an -vf "fps=10,scale=596:336" "${filename%.*}.webm";
