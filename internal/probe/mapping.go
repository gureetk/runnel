// SPDX-License-Identifier: GPL-3.0-or-later

package probe

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gureetk/runnel/internal/model"
)

// output is the part of ffprobe's JSON that is read.
type output struct {
	Format *struct {
		FormatName flex `json:"format_name"`
		Duration   flex `json:"duration"`
		BitRate    flex `json:"bit_rate"`
	} `json:"format"`
	Streams  []stream  `json:"streams"`
	Chapters []chapter `json:"chapters"`
}

type stream struct {
	Index         flex              `json:"index"`
	CodecType     flex              `json:"codec_type"`
	CodecName     flex              `json:"codec_name"`
	Profile       flex              `json:"profile"`
	Level         flex              `json:"level"`
	Width         flex              `json:"width"`
	Height        flex              `json:"height"`
	PixFmt        flex              `json:"pix_fmt"`
	AvgFrameRate  flex              `json:"avg_frame_rate"`
	ColorTransfer flex              `json:"color_transfer"`
	Channels      flex              `json:"channels"`
	ChannelLayout flex              `json:"channel_layout"`
	SampleRate    flex              `json:"sample_rate"`
	BitRate       flex              `json:"bit_rate"`
	ExtradataSize flex              `json:"extradata_size"`
	Disposition   map[string]flex   `json:"disposition"`
	Tags          map[string]string `json:"tags"`
	SideData      []struct {
		Type flex `json:"side_data_type"`
	} `json:"side_data_list"`
}

type chapter struct {
	StartTime flex              `json:"start_time"`
	EndTime   flex              `json:"end_time"`
	Tags      map[string]string `json:"tags"`
}

// flex is a JSON value that ffprobe prints as a number or as a string,
// depending on the field and its version, kept as text.
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flex(s)
		return nil
	}
	if string(b) == "null" {
		*f = ""
		return nil
	}
	*f = flex(b)
	return nil
}

func (f flex) String() string { return string(f) }

// Int is the value as an integer, and whether it is one. "N/A" is not.
func (f flex) Int() (int64, bool) {
	n, err := strconv.ParseInt(string(f), 10, 64)
	if err != nil {
		// Some counts are printed as decimals.
		x, err := strconv.ParseFloat(string(f), 64)
		if err != nil || math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return int64(x), true
	}
	return n, true
}

// Millis reads seconds, such as "2.008000", as milliseconds.
func (f flex) Millis() (int64, bool) {
	x, err := strconv.ParseFloat(string(f), 64)
	if err != nil || math.IsNaN(x) || math.IsInf(x, 0) || x < 0 {
		return 0, false
	}
	return int64(math.Round(x * 1000)), true
}

func (f flex) int() int     { n, _ := f.Int(); return int(n) }
func (f flex) int64() int64 { n, _ := f.Int(); return n }

// tag reads a tag whatever the case of its name: Matroska files often
// carry "LANGUAGE" or "TITLE" where other containers have "language".
func tag(tags map[string]string, name string) string {
	if v, ok := tags[name]; ok {
		return strings.TrimSpace(v)
	}
	for k, v := range tags {
		if strings.EqualFold(k, name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func disposition(d map[string]flex, name string) bool {
	n, _ := d[name].Int()
	return n == 1
}

// Container names the container. ffprobe names a family, such as
// "matroska,webm" or "mov,mp4,m4a,3gp,3g2,mj2"; the file's extension
// tells WebM from Matroska and QuickTime from MP4. Other formats keep the
// first name ffprobe gives: "avi", "mpegts", "asf", "mpeg".
func Container(formatName, path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case formatName == "":
		return ""
	case strings.HasPrefix(formatName, "matroska"):
		if ext == ".webm" {
			return "webm"
		}
		return "matroska"
	case strings.HasPrefix(formatName, "mov,"):
		if ext == ".mov" {
			return "mov"
		}
		return "mp4"
	}
	first, _, _ := strings.Cut(formatName, ",")
	return first
}

// MediaType is the type a file of this container is served as.
func MediaType(container string) string {
	switch container {
	case "matroska":
		return "video/x-matroska"
	case "webm":
		return "video/webm"
	case "mp4":
		return "video/mp4"
	case "mov":
		return "video/quicktime"
	case "avi":
		return "video/x-msvideo"
	case "mpegts":
		return "video/mp2t"
	case "asf":
		return "video/x-ms-asf"
	case "mpeg":
		return "video/mpeg"
	}
	return "application/octet-stream"
}

// hdr reads the kind of high dynamic range of a video stream. A Dolby
// Vision configuration record wins over the transfer function, which a
// Dolby Vision file may also carry. HDR10+ is carried frame by frame and
// cannot be seen from -show_streams; such a file reads as HDR10.
func hdr(s stream) string {
	for _, sd := range s.SideData {
		if strings.HasPrefix(strings.ToLower(sd.Type.String()), "dovi configuration") {
			return "dv"
		}
	}
	switch s.ColorTransfer {
	case "smpte2084":
		return "hdr10"
	case "arib-std-b67":
		return "hlg"
	}
	return ""
}

// mapOutput turns ffprobe's output into what the database keeps.
func mapOutput(raw output, path string, depths map[string]int) model.Probe {
	var p model.Probe
	p.Container = Container(raw.Format.FormatName.String(), path)
	p.DurationMS, _ = raw.Format.Duration.Millis()
	p.BitRate = raw.Format.BitRate.int64()

	for _, s := range raw.Streams {
		kind := s.CodecType.String()
		switch kind {
		case "video", "audio", "subtitle", "attachment":
		default:
			continue // data streams and anything else ffprobe reports
		}
		if kind == "video" && disposition(s.Disposition, "attached_pic") {
			continue // cover art: a picture, not something to play
		}
		st := model.Stream{
			Index:           s.Index.int(),
			Kind:            kind,
			Codec:           s.CodecName.String(),
			Profile:         s.Profile.String(),
			Lang:            tag(s.Tags, "language"),
			Title:           tag(s.Tags, "title"),
			Default:         disposition(s.Disposition, "default"),
			Forced:          disposition(s.Disposition, "forced"),
			HearingImpaired: disposition(s.Disposition, "hearing_impaired"),
		}
		switch kind {
		case "video":
			st.Width, st.Height = s.Width.int(), s.Height.int()
			st.PixFmt = s.PixFmt.String()
			st.BitDepth = bitDepth(st.PixFmt, depths)
			if lvl, ok := s.Level.Int(); ok && lvl >= 0 {
				l := int(lvl)
				st.Level = &l // ffprobe prints -99 when it does not know
			}
			if num, den, ok := strings.Cut(s.AvgFrameRate.String(), "/"); ok {
				n, _ := strconv.Atoi(num)
				d, _ := strconv.Atoi(den)
				if n > 0 && d > 0 {
					st.FPSNum, st.FPSDen = n, d
				}
			}
			st.HDR = hdr(s)
		case "audio":
			st.Channels = s.Channels.int()
			st.ChannelLayout = s.ChannelLayout.String()
			st.SampleRate = s.SampleRate.int()
			st.BitRate = s.BitRate.int64()
		case "attachment":
			st.Filename = tag(s.Tags, "filename")
			st.MIME = tag(s.Tags, "mimetype")
			st.Size = s.ExtradataSize.int64()
			p.AttachmentsBytes += st.Size
		}
		p.Streams = append(p.Streams, st)
	}

	for i, c := range raw.Chapters {
		ch := model.Chapter{Index: i, Title: tag(c.Tags, "title")}
		ch.StartMS, _ = c.StartTime.Millis()
		if end, ok := c.EndTime.Millis(); ok {
			ch.EndMS = &end
		}
		p.Chapters = append(p.Chapters, ch)
	}
	return p
}
