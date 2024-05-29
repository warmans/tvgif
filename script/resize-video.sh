#e.g. ./resize-video foo.avi
filename=$(basename -- $1)
echo "Converting ${filename} to ${filename%.*}.webm"
ffmpeg -i $1 -vf "fps=10,scale=596:336:force_original_aspect_ratio=decrease,pad=596:336:-1:-1:color=black" "${filename%.*}.webm"