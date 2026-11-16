#!/usr/bin/env bash
# Puts 8 demo movies in the local catalog, ready and published: short synthetic clips
# generated with ffmpeg's lavfi sources (fractals, cellular automata,
# gradients, noise, test patterns; 20-40 s, 1080p and 720p, a quiet tone as
# audio; two with invented subtitle tracks in English, Spanish and
# French), ingested with tools/devingest under invented titles, years and
# overviews, each with a 2:3 poster cut from one of its own frames, and
# published, so the demo catalogue is complete. The clips are generated into a temp dir and removed afterwards;
# nothing is ever committed. Every title, year and overview below is made up
# for this script.
#
# Normally via `make demo-seed`, which loads .env (DATABASE_URL, MEDIA_ROOT).
# It writes to $MEDIA_ROOT and that database, like `make dev-ingest`; running
# it twice adds the 8 movies twice.
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL is not set: put it in .env}"
: "${MEDIA_ROOT:?MEDIA_ROOT is not set: put it in .env}"
command -v ffmpeg >/dev/null || { echo "ffmpeg is missing (make setup checks it)" >&2; exit 2; }

work=$(mktemp -d "${TMPDIR:-/tmp}/demo-seed.XXXXXX")
cleanup() { [[ -d $work ]] && rm -r -- "$work"; }
trap cleanup EXIT

go build -o "$work/devingest" ./tools/devingest

# srt FILE LINE... writes a SubRip file: one invented line every 6 s, from 1 s.
srt() {
	local file=$1 n=0 start end
	shift
	: >"$file"
	for line in "$@"; do
		start=$((1 + n * 6))
		end=$((start + 5))
		printf '%d\n00:00:%02d,000 --> 00:00:%02d,000\n%s\n\n' $((n + 1)) "$start" "$end" "$line" >>"$file"
		n=$((n + 1))
	done
}

# seed NAME SECONDS TONE_HZ LAVFI_VIDEO TITLE YEAR OVERVIEW [SUBTITLE...]
# Each SUBTITLE is LANG:TITLE:FILE (TITLE may be empty), a SubRip file
# muxed in as a text subtitle stream, so the transcoder extracts it to WebVTT.
seed() {
	local name=$1 secs=$2 tone=$3 video=$4 title=$5 year=$6 overview=$7
	shift 7
	# Matroska: it keeps a subtitle stream's title ("SDH"), which MP4 drops.
	local clip=$work/$name.mkv
	local inputs=() maps=() meta=() i=0 lang stitle file
	for spec in "$@"; do
		IFS=: read -r lang stitle file <<<"$spec"
		inputs+=(-i "$file")
		maps+=(-map "$((i + 2)):0")
		meta+=("-metadata:s:s:$i" "language=$lang")
		[[ -n $stitle ]] && meta+=("-metadata:s:s:$i" "title=$stitle")
		i=$((i + 1))
	done
	echo "generating $name (${secs} s, $# subtitle tracks)" >&2
	ffmpeg -nostdin -loglevel error -y \
		-f lavfi -i "$video" \
		-f lavfi -i "sine=frequency=$tone:sample_rate=48000,volume=0.05" \
		"${inputs[@]}" \
		-map 0:v -map 1:a "${maps[@]}" "${meta[@]}" \
		-t "$secs" -c:v libx264 -preset veryfast -pix_fmt yuv420p -c:a aac -b:a 96k -c:s srt \
		"$clip"
	# The poster: one frame from 40 % in, the centre cut to 2:3 and scaled to
	# 1000x1500 (the largest poster width), as an admin would upload it.
	local poster=$work/$name.poster.png
	ffmpeg -nostdin -loglevel error -y -ss "$((secs * 2 / 5))" -i "$clip" -frames:v 1 \
		-vf "crop=trunc(ih*2/3/2)*2:ih,scale=1000:1500:flags=lanczos" "$poster"
	echo "ingesting \"$title\"" >&2
	"$work/devingest" --input "$clip" --title "$title" --year "$year" --overview "$overview" \
		--poster "$poster" --publish
}

# Invented subtitle lines for two of the movies, in three languages.
srt "$work/slow.en.srt" "The light is late tonight." "Violet, then blue. She is asking a question." \
	"I answer in amber. It means: I am still here." "Forty years, and we have never met." \
	"Maybe the strait is the conversation." "Goodnight, other shore."
srt "$work/slow.sdh.srt" "[WAVES ROLLING] The light is late tonight." "[LAMP HUMMING] Violet, then blue." \
	"I answer in amber. [CLICK]" "[WIND] Forty years, and we have never met." \
	"Maybe the strait is the conversation." "[FOGHORN IN THE DISTANCE] Goodnight."
srt "$work/slow.es.srt" "La luz llega tarde esta noche." "Violeta, luego azul. Me está preguntando algo." \
	"Respondo en ámbar: sigo aquí." "Cuarenta años y nunca nos hemos visto." \
	"Quizá el estrecho sea la conversación." "Buenas noches, otra orilla."
srt "$work/slow.fr.srt" "La lumière est en retard ce soir." "Violet, puis bleu. Elle pose une question." \
	"Je réponds en ambre : je suis toujours là." "Quarante ans, et jamais rencontrés." \
	"Le détroit est peut-être la conversation." "Bonne nuit, l'autre rive."
srt "$work/interference.en.srt" "Two stones. Two circles." "Where they cross, the water stands still." \
	"You call it physics." "I call it a painting that moves." "Then we are both right."

seed edge 30 220 \
	"mandelbrot=s=640x360:rate=25:maxiter=300:start_scale=3:end_scale=0.02:end_pts=30:start_x=-0.743643887037151:start_y=0.131825904205330:inner=mincol:outer=normalized_iteration_count,scale=1280:720" \
	"Edge of the Set" 2019 \
	"A survey ship drifts toward a coastline that never ends. Every time the crew draws the map, the shore grows another bay, and the bay another inlet."

seed generation 36 196 \
	"life=s=480x270:rate=25:mold=10:ratio=0.08:life_color=#c4b5fd:death_color=#1e1b4b:mold_color=#4c1d95,scale=1920:1080:flags=neighbor" \
	"The Long Generation" 2021 \
	"A colony follows four simple rules and nothing else. Some families vanish in a single season; a few, against all odds, drift on for ever."

seed rule 24 247 \
	"cellauto=rule=110:s=640x360:rate=25:scroll=1,format=rgb24,lutrgb=r=val*0.25:g=val*0.85:b=val,scale=1280:720:flags=neighbor" \
	"Rule 110" 2016 \
	"One line of cells, one rule, and a machine that turns out to compute anything. A quiet thriller about the smallest possible computer."

seed slow 40 174 \
	"gradients=s=1920x1080:rate=25:speed=0.015:n=6" \
	"Slow Light" 2023 \
	"Two lighthouse keepers on opposite shores signal to each other in colour, one shade a night, across a strait neither of them has crossed." \
	"eng::$work/slow.en.srt" "spa::$work/slow.es.srt" "fre::$work/slow.fr.srt" "eng:SDH:$work/slow.sdh.srt"

seed calibration 20 440 \
	"testsrc2=s=1280x720:rate=25" \
	"Calibration Night" 2012 \
	"The last engineer at a closing television station runs the test pattern one more time, and waits to see who is still watching."

seed carpet 28 262 \
	"sierpinski=s=1280x720:rate=25:type=carpet:jump=40,format=rgb24,lutrgb=r=val:g=val*0.62:b=val*0.2" \
	"All the Way Down" 2018 \
	"A weaver is asked for a carpet with no plain patch anywhere. Each square she finishes holds eight smaller squares still to do."

seed interference 32 330 \
	"zoneplate=s=960x540:r=25:kx2=200:ky2=200:kt2=6,scale=1920:1080" \
	"Interference" 2020 \
	"Ripples from two stones meet in a still pond. A physicist and a painter argue, beautifully, about which of them is seeing it right." \
	"eng::$work/interference.en.srt"

seed weather 34 147 \
	"perlin=s=960x540:rate=25:octaves=5:persistence=0.55:xscale=1:yscale=1:tscale=0.3,format=yuv444p,pseudocolor=p=magma,scale=1920:1080" \
	"Weather Inside" 2024 \
	"A forecaster who can read every cloud over the city cannot predict a single mood in her own house. Warm fronts, cold fronts, one long summer."

echo "demo-seed: 8 movies ingested" >&2
