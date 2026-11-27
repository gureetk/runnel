# Playback

How a file gets from disk to a moving picture, and how to keep that fast. This covers the server's side in full and the player settings the client needs. The numbers come from tests in the project's research notes, which are not published. They were measured with desktop mpv 0.37 and ffmpeg 6.1 on a 2-vCPU sandbox with software codecs. Since then the hardware form of section 4 has run by hand on real hardware, with VAAPI on an Intel GPU (section 12). Its figures are kept outside the repository (D28). Nothing here has run on a TV yet.

**Revised, 20 November 2026.** Every Runnel app plays with mpv (D42, D43), so the player settings of section 11 are for all of them. New: how a browser is fed (section 13, D39, D40), with its own tests. The targets are now section 14.

## 1. The paths

| Path | When | Start, measured | Audio switch, measured | State |
|---|---|---|---|---|
| **Direct play** | The device can decode the file. | 30–130 ms from a keyframe | 47 ms | Designed. Mechanism tested on desktop. |
| **Piped transcode** | The device cannot decode the video. | About 0.1 s with a fast encoder, 0.6 s with a slow one. About 0.15 s after a one-second warm-up. | 240–266 ms by restarting, 1.7 s when the encoder is slow | Designed. Software form tested. Hardware form run by hand on one GPU. |
| **Piped transcode with sidecar** | Same, once the sidecar file exists. | As above | 16–113 ms | Designed. Tested on desktop. Stage 10. |
| **In a browser** | A page plays it: two piped MP4 streams, video and audio. | 0.14–0.32 s with the video copied, about 0.6 s re-encoded, in Chrome | 88–171 ms | Designed. Tested in Chrome only. Stage 7. Section 13. |

For comparison, the same slow encoder cut into 3-second HLS segments took 2.3 s to start.

## 2. Rules

Each rule comes from a measurement.

1. Play the original file whenever the device can decode it.
2. The client decides. The item detail carries every fact it needs, so there is no negotiation call before playback.
3. Resume with a keyframe seek, on the server and in the player. An exact seek decodes and discards everything from the previous keyframe: 916 ms against 123 ms in the test.
4. Pipe the transcode. Do not cut it into segments: a segment is only usable when complete, so its length divided by the encode speed is dead time.
5. Re-encode video only. Copy audio, subtitles and attachments.
6. Give the muxer `-max_interleave_delta 500000` whenever a subtitle stream is copied. Without it a subtitle track with nothing to say held the first packet back by 4.9 s.
7. Never give ffmpeg an output time limit (`-t`, `-to`) together with a copied subtitle stream. It kept decoding and held every frame: 3.3 GB and 6.1 GB in two runs.
8. Do not switch tracks inside a stream that cannot seek. mpv starts the new track where its buffer ends, which was 223 s away in the test.
9. Keep the player's readahead on a piped stream to a few seconds. Unbounded, the encoder was 222 s ahead after 10 s of playback.
10. Start a transcode when its item has focus, not when Play is pressed (section 6).
11. Remember track choices per series on the server, so the next episode starts on the right tracks.
12. Kill ffmpeg the moment the client disconnects, with SIGKILL or by closing its pipe. SIGTERM can hang: ffmpeg tries to flush into a pipe nobody reads.
13. Nothing in the background competes with the first seconds of a playback. Probes, sidecar builds and image scaling pause while a session starts.

## 3. Direct play

**Server.** `GET /files/{id}/raw` opens the file and hands it to the standard library's ranged file server. On Linux that copies from the page cache to the socket inside the kernel. Memory did not grow while streaming in the prototype.

- `ETag` is the file's fingerprint, so a player that reconnects resumes its range.
- No write timeout on this route. A paused player may sit on a connection for hours.
- A request for a file with `missing_since` set, or on a root marked unavailable, is `503 unavailable` without touching the disk. So is one for a file that cannot be opened, which the next scan marks missing.
- Checked in the development sandbox: `strace` showed `sendfile` carrying everything after the first 512 bytes of each response, whole files and ranges alike, through the server's middleware.

**Warm-up for direct play.** `prepare` with `mode: direct` reads the first 4 MiB and the last 1 MiB of the file and discards them. That pulls the header, the attachments and usually the index into the page cache, and spins up a sleeping disk before Play is pressed. It answers before it reads, so the request never waits for a disk to spin up.

**Player.** See section 11.

## 4. Piped transcode

One ffmpeg child per stream. Its standard output is relayed to the HTTP response as it arrives, flushed after every read.

### 4.1 The command

Built as an argument array, never as a shell string. In order:

| Part | Arguments | Why |
|---|---|---|
| Quiet | `-hide_banner -loglevel error -nostdin` | Errors only, on standard error, which the server logs. |
| Hardware input, when used | `-hwaccel vaapi -hwaccel_output_format vaapi -noautorotate` on the first rung of the ladder (4.3), `-vaapi_device <device>` on the second | Decode on the GPU and keep frames there. These three are taken from a command that is known to work with VAAPI, and ran on real hardware as they are: ffmpeg found the render device. A device other than `/dev/dri/renderD128` is named with `-hwaccel_device`, which is untested. |
| Seek | `-ss <seconds>` and, for `seek=key`, `-noaccurate_seek` | Before `-i`, so ffmpeg seeks in the file. `-noaccurate_seek` starts output at the keyframe. |
| Input | `-i <path>` | |
| Streams | `-map 0:v:0`, then audio, then `-map 0:s?` and `-map 0:t?` as asked | Audio is `-map 0:a?` for all, nothing for none, `-map 0:<idx>` for one. The `?` keeps a file without audio, subtitles or attachments from failing: without it ffmpeg stops at once on a file with no audio [checked with ffmpeg 6.1]. |
| Copy by default | `-c copy` | Everything is copied unless overridden. |
| Video encoder | See 4.2 | Overrides the copy for video. |
| Timestamps | `-copyts` | Output keeps the file's own timestamps, so the player's position is the real one. |
| Interleaving | `-max_interleave_delta 500000` | Rule 6. |
| Output | `-f matroska -flush_packets 1 pipe:1` | Every packet leaves ffmpeg as soon as it is muxed. |

The software form of this command is the one that was tested:

```
ffmpeg -hide_banner -loglevel error -nostdin \
  -ss 65 -noaccurate_seek -i INPUT.mkv \
  -map 0:v:0 -map 0:a? -map 0:s? -map 0:t? \
  -c copy -c:v libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p \
  -copyts -max_interleave_delta 500000 \
  -f matroska -flush_packets 1 pipe:1
```

### 4.2 Video encoder arguments

| Request | Arguments | State |
|---|---|---|
| `v=hevc`, `depth=10`, VAAPI | `-vf scale_vaapi=format=p010 -c:v hevc_vaapi -profile:v main10 -rc_mode CQP -qp 24 -bf 0` | Ran on real hardware (section 12). |
| `v=hevc`, `depth=8`, VAAPI | `-vf scale_vaapi=format=nv12 -c:v hevc_vaapi -profile:v main -rc_mode CQP -qp 24 -bf 0` | Ran on real hardware (section 12). Jellyfin uses the same filter and encoder, with a bitrate target. |
| `v=h264`, VAAPI | `-vf scale_vaapi=format=nv12 -c:v h264_vaapi -profile:v high -rc_mode CQP -qp 23 -bf 0` | Untested. |
| `v=h264`, software | `-c:v libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p` | Tested. 220 MB of memory at 1080p. |
| `max_h` set | Add `w=-2:h=<max_h>` to `scale_vaapi`, put `scale=-2:<max_h>` before `hwupload` on the second rung, or `-vf scale=-2:<max_h>` in software. Only when the source is taller. | Untested. |

Choices behind these:

- **Quality-based rate control.** On a home network bandwidth is not the limit, so the encoder is told a quality, not a bitrate.
- **No B-frames.** Fewer frames wait inside the encoder before the first packet leaves.
- **10-bit stays 10-bit** when the client can decode HEVC Main 10.
- **Software fallback is H.264 only.** Software HEVC ran below real time in the tests.

### 4.3 The fallback ladder

For each request the server tries, in order, and moves down when ffmpeg exits before its first output. Nothing has been sent to the client by then:

1. Hardware decode and hardware encode.
2. Software decode and hardware encode (`-vf format=nv12,hwupload`, or `format=p010,hwupload` for 10-bit output, in place of `scale_vaapi`). Ran on real hardware for HEVC 10-bit (section 12).
3. Software decode and software H.264, if the client asked for `h264`. A request names one codec (`04-api.md`, section 7.3), so a request for HEVC never gets H.264.

A failure is remembered for the process's life by the source's codec, profile and bit depth and the target asked for, so the next request starts on the rung that works. When no rung is left, the request is refused with `500 encode_failed` and what ffmpeg said last, and the server logs each rung that failed. A child killed for its memory before its first output fails the request without blaming its rung, since the next would hold as much.

### 4.4 Memory and limits

| Limit | Default | Notes |
|---|---|---|
| Transcode slots | 1 | Streams running at once. A request beyond that is `503 busy`. |
| Warm-ups held | 1 | A real request without a ticket evicts a held warm-up. |
| ffmpeg resident memory | 1024 MB | Read from `/proc/<pid>/statm` every 2 s, from the child's start. Above the limit the child is killed, the stream ends, and the server logs it. This does not see GPU memory. |

What one ffmpeg held in the tests, same 1080p 10-bit input, software codecs:

| Pipeline | Peak memory |
|---|---|
| Decode only | 158 MB |
| Decode, x264 `ultrafast` | 220 MB |
| Decode, x264 `veryfast` | 380 MB |
| Decode, x265 `veryfast` | 496 MB |

Launch style made no difference (387 MB as a stock-server-style HLS command, 382 MB piped). With hardware codecs the frames live in GPU memory, which on an integrated GPU is system RAM that `ps` does not show. It was measured by hand on real hardware (section 12), and the figures are kept outside the repository (D28).

## 5. Sessions

A session is one playback as the client sees it. The client makes its id and sends it with the transcode request and with every progress report.

| State | Meaning | Leaves when |
|---|---|---|
| `warming` | ffmpeg is running for a `prepare`. Its output goes to a buffer. | The buffer fills (`held`), it is claimed, or 30 s pass. |
| `held` | The buffer is full and ffmpeg is blocked on its pipe. No CPU is used. | Claimed, replaced by another `prepare`, evicted, or 30 s pass. |
| `streaming` | Output is going to a client. | The file ends or the client disconnects. |
| `ended` | ffmpeg is gone. | — |

- The server holds no session state on disk. A restart ends every stream; clients ask again.
- A browser's video stream and audio stream are two requests in one session (section 13).
- For each stream the server records the time from request to first byte sent, the encoder used and whether a warm-up was claimed. They go to `play_log` with the client's own timings, when the session's first report with timings arrives (`04-api.md`, section 7.6). The last stream of each session is kept for that until 30 minutes after it ends.
- ffmpeg's output is relayed as it comes, 64 KiB at a time at most, and flushed after every write. ffmpeg is killed with SIGKILL when the stream ends for any reason: the file's end or the client gone.
- On shutdown the server kills every child before it stops taking requests, which ends the streams they fed.

## 6. Warm-up

Measured with a slow encoder (1.7× real time), 4 MiB held:

| When the client asks | First video packet | mpv playing |
|---|---|---|
| ffmpeg starts on request | 528–555 ms | 569–621 ms |
| 0.25 s after the warm-up began | 272–295 ms | 247–401 ms |
| 1 s after | 176–188 ms | 131–156 ms |
| 3 s after | 85–86 ms | 128–194 ms |

How it works:

1. `prepare` starts the same ffmpeg command a transcode request would.
2. A goroutine reads its output into a bounded buffer. When the buffer is full the goroutine stops reading, the pipe fills, and ffmpeg blocks.
3. The transcode request that carries the ticket is handed the buffer, then the live output.

Rules:

- **Buffer size is `attachments_bytes` plus 4 MiB.** A buffer smaller than the attachments blocks ffmpeg before it has written any video. With a 742 KiB font and a 512 KiB buffer the gain shrank from about 350 ms to about 120 ms.
- **A waiting ffmpeg keeps its memory.** 372–376 MB in the software 1080p test. That is why one warm-up is held at a time and for 30 seconds at most.
- **Matching is exact.** The ticket is tied to the file and the normalised parameters. A request that differs starts fresh.
- **No time limits.** The warm-up is held back by its full pipe. Rule 7.
- **It needs about a second of dwell** to pay off in full with a hardware encoder, which can take most of a second to produce its first output. That figure is to be measured (section 12).

## 7. Seeking, resuming and switching audio

| Action | Direct play | Transcode |
|---|---|---|
| Resume | The player seeks to the keyframe at or before the saved position. | `start_ms` with `seek=key`. |
| Seek inside the player's buffer | The player's own seek. | The player's own seek. The buffer is small, so this covers short hops. |
| Seek elsewhere | The player's own seek, by range request. | A new request with a new `start_ms`. `seek=key` for chapter jumps and long skips, `seek=exact` when the picture must land where asked. |
| Switch audio or subtitles | The player switches. 47 ms. | With the sidecar: the player switches, 16–113 ms. Without: a new request at the current position with `seek=exact`, and the new track chosen as the stream loads. |

An exact seek costs decode time in proportion to the distance from the previous keyframe. With hardware decoding that is a fraction of a second for a 10-second keyframe interval, which is why the restart can afford it. That figure is an estimate until measured.

## 8. What the server can encode

At start, before the server listens, and when an admin asks (`POST /admin/encoders/check`, `04-api.md`, section 8.7), the server finds out what works here and reports it in `GET /server`. `runnel doctor` runs the same check and prints it:

1. Does the configured ffmpeg list `hevc_vaapi`, `h264_vaapi` and `libx264`?
2. Does a one-second encode of a test pattern succeed for each of: HEVC 10-bit, HEVC 8-bit and H.264 on the GPU, and H.264 in software? Each encode uses the encoder arguments of section 4.2, and has 3 seconds.

   ```
   ffmpeg -hide_banner -loglevel error -nostdin -vaapi_device /dev/dri/renderD128 \
     -f lavfi -i testsrc2=size=1280x720:rate=24 -t 1 \
     -vf format=p010,hwupload -c:v hevc_vaapi -profile:v main10 -rc_mode CQP -qp 24 -bf 0 -f null -
   ```

3. Hardware decoding is not probed. It is discovered per file through the fallback ladder.

The result is cached in memory, with the time of the check. A target that does not work says why: ffmpeg's first line of error, an encoder this ffmpeg lacks, a render device that is not there, or `RUNNEL_HWACCEL=none`, which skips the GPU and offers software H.264 only. `auto` and `vaapi` differ only in the log: with `vaapi`, a GPU that encodes nothing is a warning. A check on request is refused while a transcode runs. `runnel doctor` asks no running server, and also says whether its user can open the render device.

On Intel GPUs both VAAPI and QSV are available. Jellyfin's guide prefers QSV on recent generations. One FFmpeg bug report measured QSV holding more than twice the GPU memory of VAAPI for the same 4K transcode. Run by hand on real hardware, QSV was the slower of the two and held more memory (section 12). The server uses VAAPI, and QSV is not built (D50).

## 9. The sidecar

`ffmpeg -i INPUT.mkv -map 0 -map -0:v -c copy sidecar.mkv`: the original minus its video.

- It is a real file with its own index, so the player can seek in it. That is what makes local track switching work during a transcode. A stream remuxed on the fly would fail the way rule 8 describes.
- Building reads the whole original once. In the test that took 3–4 ms per second of video.
- Built by a background task: when a transcode starts for a file, for that file and for the next episode. Not built for files that are only ever played directly.
- Stored under the data directory as `sidecar/<file id>-<fingerprint>.mkv`. A cache with a size cap, oldest use first out. A changed file has a new fingerprint and so a new sidecar.
- With the sidecar in use the transcode is asked for `audio=none&subs=none&fonts=0`, and the player adds the sidecar as an external audio and subtitle source.
- A browser does not use it. It has an audio stream of its own, which does the same job without a file built in advance (section 13).

Confirmed on desktop: one sidecar with audio, subtitles and a font worked, the first subtitle appeared at 60.02 s against 60.00 s in the script, and audio stayed in sync at four positions.

## 10. Subtitles and fonts

- Subtitles are never burned in.
- In direct play the player reads subtitles and fonts from the file.
- In a piped transcode they are copied in, with rule 6. Attachments are written before the first video packet, so the player downloads every font before the first frame. Harmless for one small font, costly for a release with tens of megabytes of them. The sidecar moves them out of the stream. `fonts=0` is there for a client that already has them.
- A font cache on the client, keyed by hash, is a later step.
- Image-based subtitles (PGS, VobSub) are copied the same way. Untested.
- In a browser the page draws text subtitles itself, from files the server extracts. Image-based subtitles are not shown there in version 1 (section 13).

## 11. Player settings

For an mpv-based client, which every Runnel app is.

| Setting | Direct play | Piped transcode | Why |
|---|---|---|---|
| `hr-seek` | `no` for the load that resumes | not used | Rule 3. mpv's default for a start position is an exact seek. |
| `cache-pause-initial` | `no` | `no` | Waiting for an initial buffer suits live TV. For a library file that wait is start-up delay, and a transcode at 3.7× needs 0.8 s to produce 3 s of video. |
| `demuxer-readahead-secs` | 10 or more | 3 to 5 | Rule 9. |
| `rebase-start-time` | default | `no` | The stream keeps the file's timestamps. Without this mpv reports the position from zero. |
| `demuxer-max-back-bytes` | default | 32 MiB or more | Lets short backward seeks stay inside the buffer. |
| Duration | from the file | from `files[].duration_ms` | A piped Matroska stream carries no duration. |
| Second source | none | `audio-add <sidecar url>` and subtitles from the same file, when ready | Section 9. |
| Decoding | hardware where the device has a decoder, software for what it has none for, where the device's setting allows that (D37) | hardware | When hardware decoding draws straight to the surface there is no software fallback, so a codec without a hardware decoder must be opened in software directly. |

## 12. To check on real hardware

In this order, before building on any number in this document:

1. **The hardware command by hand.** Run the HEVC 10-bit form of section 4 on one AV1 file. Does it run? Time to the first megabyte: `time (ffmpeg … pipe:1 2>/dev/null | head -c 1000000 >/dev/null)`. The `speed=` it reaches. Its memory: `ps -o rss,args -C ffmpeg`, and the drop in `available` from `free -m`.
2. **Variants**, one change at a time: without `-bf 0`; with `-async_depth 1`; QSV in place of VAAPI; 8-bit output. Keep what lowers time to first output without costing memory.
3. **The output on the TV.** HEVC Main 10 in Matroska over HTTP, in an mpv-based player on a TV, with hardware decoding.
4. **Warm-up on the real network.** Claim after 0.25 s, 1 s and 3 s.
5. **Audio switch by restart.** From track change to sound.
6. **Sidecar in libmpv on a TV.** Load a video-only stream with `rebase-start-time=no`, run `audio-add <sidecar url> select`, switch `aid` after ten seconds. A healthy switch logs "starting audio playback" within a fraction of a second. A broken one logs "delaying audio start".
7. **Subtitles.** ASS with fonts, copied into the stream and from the sidecar.
8. **Disconnect.** Stop the player mid-stream. ffmpeg must be gone within a second.
9. **Rules 6, 7 and 12 on the ffmpeg version the server will use.** They were found on 6.1.
10. **Browsers.** The tests of section 13.4 in Firefox, and in Safari on a Mac and on an iPhone, with real files and with the devices' hardware decoders. What Safari says about Opus in MP4.

**Items 1, 2 and 9 ran on 27 November 2026**, by hand on real hardware: VAAPI on an Intel GPU, ffmpeg 8.1, an AV1 10-bit file. Their figures are kept outside the repository (D28). What they settled:

- Item 1: the HEVC 10-bit form of section 4 runs as written, decoding and encoding on the GPU.
- Item 2: without `-bf 0`, and with `-async_depth 1`, the first output came no sooner, so the form stays as it is. QSV was slower and held more memory (D50). 8-bit output runs. The second rung of the ladder (4.3) ran too.
- Item 9: rules 6 and 12 still hold on ffmpeg 8.1. Rule 7's failure did not show there. The rule stays, since keeping it costs nothing.
- `speed=` printed N/A for every form on that ffmpeg, so a check counts frames instead.

## 13. In a browser

A page cannot use the paths above as they are. Most browsers do not open Matroska, none draws ASS subtitles, and a `<video>` element cannot seek in a piped stream. D39 and D40 give browsers a delivery of their own. The calls are in `04-api.md`, section 7.9.

### 13.1 The shape

- **The page decides** (rule 2). It asks the browser whether it can decode the video and the chosen audio stream, by the names in `mp4_codecs`, and asks the server to copy or to re-encode each.
- **Two streams, two requests.** Video only, and one audio track only, each as fragmented MP4 from its own ffmpeg child, relayed as it is produced. The page appends them to two source buffers of one `MediaSource`.
- **Copy whenever the browser can decode it.** A copy uses no transcode slot. Its child held 56 to 58 MB and copied a 150-second file in a quarter of a second of processor time.
- **An audio switch replaces the audio stream only.** The video stream keeps running, copied or re-encoded. This does for a browser what the sidecar does for mpv, with no file built in advance.
- **A seek outside the buffer** is two new requests. Copied video starts at the keyframe at or before the position.
- **Subtitles are drawn by the page**, by libass compiled to WebAssembly, on a canvas over the video. The server reads through a file once, on the first request, and keeps every text track and attachment beside the sidecars. Later requests are files from disk.

### 13.2 The commands

Video, copied:

```
ffmpeg -hide_banner -loglevel error -nostdin \
  -ss 512 -copyts -i INPUT.mkv -map 0:v:0 -c copy \
  -avoid_negative_ts disabled \
  -movflags +frag_keyframe+empty_moov+default_base_moof+frag_discont -frag_duration 500000 \
  -f mp4 pipe:1
```

Audio is the same command with `-map 0:<idx>` in place of the video, and `-c:a aac -b:a 160k` in place of the copy when the browser cannot decode the track. A re-encode of the video takes its encoder arguments and its fallback ladder from section 4, with `-frag_duration 200000` and this output in place of Matroska.

What each part is for, as the tests showed:

- `-avoid_negative_ts disabled` and `+frag_discont`, both, keep the file's own timestamps in the fragments. With either one missing the stream's clock started at zero.
- `+empty_moov+default_base_moof` give Media Source Extensions what they need: an initialisation segment first, then fragments that stand alone.
- `-frag_duration`: a fragment leaves ffmpeg only when it is complete. Half a second for a copy, which reads fast, and a fifth of a second for a re-encode (P8).
- Rule 7 holds here as well. With `-copyts`, an output `-t` wrote nothing at all.
- AAC was tested in stereo only. More channels are untested.

### 13.3 What a browser can decode

For planning. The page always asks the browser at run time.

| | Chrome, Edge | Firefox | Safari |
|---|---|---|---|
| H.264, AAC | yes | yes | yes |
| AV1 | yes, in software too | yes | only on devices with a hardware AV1 decoder |
| HEVC | where the system has a hardware decoder | the same, since Firefox 137 | yes |
| Opus in MP4 | yes | yes | reports disagree. Ask with both `opus` and `Opus`, and fall back to AAC. |
| Media Source Extensions | yes | yes | yes. On an iPhone it is `ManagedMediaSource`, from iOS 17.1, and needs `disableRemotePlayback` set on the video element. |

Checked on 20 November 2026 against: WebKit's post on Safari 17.1 (`webkit.org/blog/14735`); Mozilla's HEVC bug 1842838 and its staff's answers on `connect.mozilla.org`; the Chromium HEVC guide at `github.com/StaZhu/enable-chromium-hevc-hardware-decoding`; Bitmovin's account of Apple's AV1 support. The Chrome column was also measured (13.4). Safari's own `<video>` needs byte ranges from the server (Apple, "Creating Video"), which is one reason a piped stream is not given to it as a plain `src`.

`mp4_codecs` is built from what the probe stores: codec, profile, level and bit depth. For example `av01.0.08M.10` for AV1 Main, level 4.0, 10-bit, `avc1.640028` for H.264 High at level 4.0, `opus`, `mp4a.40.2`. The full mapping is written with the web player, at stage 7. The probe stores `level` from stage 1, so no file needs probing twice.

### 13.4 Tests

20 November 2026. Chrome for Testing 153, headless, decoding in software, on the 2-vCPU sandbox, with ffmpeg 6.1.1 and the synthetic files of the earlier tests: AV1 with a keyframe every 10 seconds, at 360p and at 1080p 10-bit, three Opus tracks, one ASS track, one attached font. A page fed by a small test server. Times are on the page's clock, from the start of a test to the event.

| Test | Result |
|---|---|
| Video and audio copied, as one stream, from the start | First frame after 144 ms at 1080p, 215 ms at 360p. The server's first byte after 50 to 70 ms. |
| The same as two streams | First frame after 324 ms. |
| Video re-encoded to H.264 (`veryfast`, 1080p), two streams | First frame after 540 to 605 ms. |
| Starting in the middle, copied, landing on the exact position | 1.5 s when the position is 5 s after its keyframe, 2.8 s at 10 s after (1080p). The browser decodes everything since the keyframe, at about 90 frames a second here. |
| Seek by new requests, copied, landing exactly | 0.46 s at 360p (9.8 s after its keyframe), 2.1 s at 1080p (6.9 s after). |
| Audio switch by restarting one combined stream | 1.9 s copied, 1.6 s re-encoded with an exact seek on the server. |
| Audio switch with two streams | The new track was buffered after 88 ms with the video copied, and after 171 ms with the video being re-encoded. The picture did not stop. With the audio re-encoded to AAC: 462 ms, and one stall of 193 ms. |
| Memory of a child that copies | 56 to 58 MB at its peak, for video, audio or both. |
| Memory of the H.264 child | 310 MB with `veryfast`, 186 MB with `ultrafast` at 720p. |
| A piped stream as a plain `src`, without Media Source Extensions | Plays in Chrome. Cannot be sought, and has no duration. |
| The original Matroska file as a plain `src` | Chrome plays it by range requests: first frame after 44 to 58 ms, and it can seek. It offers the page no way to choose an audio track. Not used in version 1. |
| ASS with an attached font, drawn by `libass-wasm` 4.1.0 | On a plain-HTTP page: ready after 97 ms, the line drawn 59 ms after the time changed, nothing drawn outside the line's time. Its files come to 2.8 MB before compression. |
| What a plain-HTTP page has | Media Source Extensions, IndexedDB, workers. Not the camera, the clipboard API, service workers, `crypto.subtle` or `crypto.randomUUID` (D45). |

Not shown by these tests: Firefox, Safari on a Mac or an iPhone, any hardware decoder or encoder, real files, a real network. Section 12, item 10.

### 13.5 Rules for the page

1. Resume and long seeks play from the keyframe the stream starts at. Short hops land exactly. This is rule 3 again: landing exactly costs the decode of everything since the keyframe.
2. Keep about 30 seconds buffered ahead, then stop reading. The pipe fills and ffmpeg waits (rule 9).
3. On an iPhone use `ManagedMediaSource`, and set `disableRemotePlayback` on the video element.
4. Load the subtitle renderer only when a styled track is shown.
5. Hold the last frame through a seek, with the control that was used still visible (`10-clients.md`, section 2).
6. Need nothing that a plain-HTTP page lacks (D45).

## 14. Targets

Measured on real hardware, from `play_log`:

| | Target |
|---|---|
| Direct play, Play pressed to first frame | 0.5 s |
| Transcode, request to first byte sent | 0.8 s cold |
| Transcode, Play pressed to first frame | 1.5 s cold, 0.5 s after a warm-up |
| Audio switch during a transcode | 1.5 s by restart, 0.15 s with the sidecar |
| In a browser, Play pressed to first frame | 0.5 s with the video copied, 1.5 s re-encoded |
| In a browser, audio switch | 0.5 s |
| ffmpeg left running after a client disconnects | none after 1 s |
