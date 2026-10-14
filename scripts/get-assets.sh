#!/usr/bin/env bash
# get-assets.sh downloads what talkie needs to run beyond the Go code: ONNX
# Runtime and the Parakeet and Kokoro model files, pinned and checked against
# SHA256. Files already present and correct are kept, so it can be run again
# after an interrupted download.
#
#   scripts/get-assets.sh [-d DIR] [--cuda] [--fp32] [--no-ort]
#
#   -d DIR    where to put them (default: ./assets)
#   --cuda    ONNX Runtime's CUDA 12 build, to run speech on an NVIDIA GPU;
#             it also needs CUDA 12 and cuDNN 9 installed
#   --fp32    Parakeet's FP32 files (2.5 GB) instead of INT8 (670 MB); a
#             little more accurate, best with --cuda
#   --no-ort  skip ONNX Runtime, for one installed another way (for example
#             a Jetson build on an Orin)
#
# ONNX Runtime's release builds are for Linux x86-64; elsewhere, install it
# yourself and use --no-ort.
set -euo pipefail

dir=assets
cuda=0
fp32=0
ort=1
while [ $# -gt 0 ]; do
	case "$1" in
	-d) dir=$2; shift 2 ;;
	--cuda) cuda=1; shift ;;
	--fp32) fp32=1; shift ;;
	--no-ort) ort=0; shift ;;
	-h | --help) sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) echo "unknown option $1 (see --help)" >&2; exit 2 ;;
	esac
done

for tool in curl sha256sum tar; do
	command -v "$tool" >/dev/null || { echo "get-assets: $tool is required" >&2; exit 1; }
done

ort_version=1.22.0
# ONNX Runtime v1.22.0 release archives.
ort_cpu=onnxruntime-linux-x64-${ort_version}.tgz
ort_cpu_sha=8344d55f93d5bc5021ce342db50f62079daf39aaafb5d311a451846228be49b3
ort_gpu=onnxruntime-linux-x64-gpu-${ort_version}.tgz
ort_gpu_sha=2a19dbfa403672ec27378c3d40a68f793ac7a6327712cd0e8240a86be2b10c55

# Parakeet TDT 0.6B v3, as pinned by go-parakeet v0.1.0 (ASSETS.md, SHA256SUMS).
parakeet_url=https://huggingface.co/istupakov/parakeet-tdt-0.6b-v3-onnx/resolve/8f23f0c03c8761650bdb5b40aaf3e40d2c15f1ce
parakeet_common=(
	"95d330fef1070f22cc4d4e380fbf5913253b44f7b71fabae997f6cdf65fe1714 README.md"
	"a9fde1486ebfcc08f328d75ad4610c67835fea58c73ba57e3209a6f6cf019e9f nemo128.onnx"
)
parakeet_int8=(
	"6139d2fa7e1b086097b277c7149725edbab89cc7c7ae64b23c741be4055aff09 encoder-model.int8.onnx"
	"eea7483ee3d1a30375daedc8ed83e3960c91b098812127a0d99d1c8977667a70 decoder_joint-model.int8.onnx"
)
parakeet_fp32=(
	"98a74b21b4cc0017c1e7030319a4a96f4a9506e50f0708f3a516d02a77c96bb1 encoder-model.onnx"
	"9a22d372c51455c34f13405da2520baefb7125bd16981397561423ed32d24f36 encoder-model.onnx.data"
	"e978ddf6688527182c10fde2eb4b83068421648985ef23f7a86be732be8706c1 decoder_joint-model.onnx"
)

# Kokoro-82M v1.0, as pinned by go-kokoro v0.3.0 (ASSETS.md, SHA256SUMS).
kokoro_url=https://github.com/thewh1teagle/kokoro-onnx/releases/download/model-files-v1.0
kokoro=(
	"7d5df8ecf7d4b1878015a32686053fd0eebe2bc377234608764cc0ef3636a6c5 kokoro-v1.0.onnx"
	"bca610b8308e8d99f32e6fe4197e7ec01679264efed0cac9140fe9c29f1fbf7d voices-v1.0.bin"
)

# fetch downloads url to path unless path already has sha, then checks it.
fetch() {
	local url=$1 path=$2 sha=$3
	if [ -f "$path" ] && echo "$sha  $path" | sha256sum -c --status -; then
		echo "ok        $path"
		return
	fi
	echo "download  $path"
	mkdir -p "$(dirname "$path")"
	curl -fL# --retry 3 -C - -o "$path.part" "$url" || curl -fL# --retry 3 -o "$path.part" "$url"
	if ! echo "$sha  $path.part" | sha256sum -c --status -; then
		rm -f "$path.part"
		echo "get-assets: $path does not match its SHA256; try again" >&2
		exit 1
	fi
	mv "$path.part" "$path"
}

# fetchAll fetches "sha name" entries from base into subdir.
fetchAll() {
	local base=$1 subdir=$2
	shift 2
	local entry
	for entry in "$@"; do
		fetch "$base/${entry#* }" "$dir/$subdir/${entry#* }" "${entry%% *}"
	done
}

mkdir -p "$dir"
if [ "$ort" = 1 ]; then
	if [ "$cuda" = 1 ]; then archive=$ort_gpu sha=$ort_gpu_sha; else archive=$ort_cpu sha=$ort_cpu_sha; fi
	fetch "https://github.com/microsoft/onnxruntime/releases/download/v${ort_version}/$archive" "$dir/$archive" "$sha"
	tar -xzf "$dir/$archive" -C "$dir"
	ln -sfn "${archive%.tgz}" "$dir/onnxruntime"
fi
fetchAll "$parakeet_url" parakeet "${parakeet_common[@]}"
if [ "$fp32" = 1 ]; then
	fetchAll "$parakeet_url" parakeet "${parakeet_fp32[@]}"
else
	fetchAll "$parakeet_url" parakeet "${parakeet_int8[@]}"
fi
fetchAll "$kokoro_url" kokoro "${kokoro[@]}"

abs=$(cd "$dir" && pwd)
ortlib=/path/to/libonnxruntime.so
[ "$ort" = 1 ] && ortlib=$abs/onnxruntime/lib/libonnxruntime.so
flags="-ort $ortlib -parakeet $abs/parakeet -kokoro $abs/kokoro"
[ "$cuda" = 1 ] && flags="-cuda $flags"
[ "$fp32" = 0 ] && flags="$flags -int8"
cat <<EOF

Done. Train talkie to your room once, then talk to it:

  go run ./cmd/talkie-demo -train -ort $ortlib -kokoro $abs/kokoro
  go run ./cmd/talkie-demo $flags

For the tests:

  export TALKIE_TEST_ORT=$ortlib TALKIE_TEST_PARAKEET=$abs/parakeet TALKIE_TEST_KOKORO=$abs/kokoro
EOF
