// SPDX-License-Identifier: GPL-3.0-or-later

package tmdb

import (
	"net/url"
	"strconv"
	"strings"
)

// Language is the language every request asks TMDB's text in, and Country
// the country whose certification is kept (docs/06-library-and-metadata.md,
// section 6.3). TMDB's own default language is en-US.
const (
	Language = "en-US"
	Country  = "US"
)

// imageLanguages are the images' languages a details request asks for:
// TMDB filters images by the request's language, and most backdrops have
// none (section 6.4 there).
const imageLanguages = "en,null"

// What each details request appends. provider_cache keeps an answer by its
// request, so these lists are part of every cached answer's key: a change
// to one fetches every answer again.
const (
	movieAppends  = "credits,external_ids,images,release_dates"
	seriesAppends = "aggregate_credits,alternative_titles,content_ratings,external_ids,images"
)

// MoviePath, SeriesPath and SeasonPath are the details requests' paths.
func MoviePath(id string) string              { return "/movie/" + id }
func SeriesPath(id string) string             { return "/tv/" + id }
func SeasonPath(id string, season int) string { return "/tv/" + id + "/season/" + strconv.Itoa(season) }

// FindPath is the request that finds TMDB's title for another provider's
// id.
func FindPath(id string) string { return "/find/" + url.PathEscape(id) }

// MovieQuery is a film's details request's query.
func MovieQuery() url.Values {
	return url.Values{"append_to_response": {movieAppends}, "include_image_language": {imageLanguages}, "language": {Language}}
}

// SeriesQuery is a series' details request's query.
func SeriesQuery() url.Values {
	return url.Values{"append_to_response": {seriesAppends}, "include_image_language": {imageLanguages}, "language": {Language}}
}

// SeasonQuery is a season's request's query.
func SeasonQuery() url.Values { return url.Values{"language": {Language}} }

// FindQuery is the query that finds a title by another provider's id:
// source is imdb_id or tvdb_id.
func FindQuery(source string) url.Values {
	return url.Values{"external_source": {source}, "language": {Language}}
}

// ImageBase is where TMDB's images are, before a size and an image's
// path: /configuration's secure_base_url (section 6.4 there).
const ImageBase = "https://image.tmdb.org/t/p/"

// Image is one of the images that the details append.
type Image struct {
	Path     string  `json:"file_path"`
	Language *string `json:"iso_639_1"`
	Width    int     `json:"width"`
	Votes    float64 `json:"vote_average"`
}

// Images are the images that the details append: the server reads only
// the logos, since the poster and the backdrop are the details' own.
type Images struct {
	Logos []Image `json:"logos"`
}

// Logo is the logo to show: a PNG, since an SVG is not decoded, in the
// request's language first, then without one, then any, the best voted
// first. "" when there is none.
func (im Images) Logo() string {
	best, rank := "", 4
	var votes float64
	for _, l := range im.Logos {
		if !strings.HasSuffix(strings.ToLower(l.Path), ".png") {
			continue
		}
		r := 2
		switch {
		case l.Language != nil && *l.Language == language2:
			r = 0
		case l.Language == nil:
			r = 1
		}
		if r < rank || r == rank && l.Votes > votes {
			best, rank, votes = l.Path, r, l.Votes
		}
	}
	return best
}

// language2 is Language's language alone, as images name theirs.
const language2 = "en"

// Named is something TMDB names: a genre, an alternative title.
type Named struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// Movie is what the server reads of a film's details, with what they
// append (section 6.4 there). Credits and images are read by their own
// parts.
type Movie struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	Overview      string  `json:"overview"`
	ReleaseDate   string  `json:"release_date"`
	Runtime       int     `json:"runtime"`
	VoteAverage   float64 `json:"vote_average"`
	VoteCount     int     `json:"vote_count"`
	Genres        []Named `json:"genres"`
	ImdbID        string  `json:"imdb_id"`
	PosterPath    string  `json:"poster_path"`
	BackdropPath  string  `json:"backdrop_path"`
	Images        Images  `json:"images"`
	ExternalIDs   struct {
		ImdbID string `json:"imdb_id"`
	} `json:"external_ids"`
	ReleaseDates struct {
		Results []struct {
			Country  string `json:"iso_3166_1"`
			Releases []struct {
				Certification string `json:"certification"`
				Type          int    `json:"type"`
			} `json:"release_dates"`
		} `json:"results"`
	} `json:"release_dates"`
}

// certificationTypes are TMDB's release types in the order a film's
// certification is taken from: theatrical, limited, digital, physical,
// TV, premiere.
var certificationTypes = []int{3, 2, 4, 5, 6, 1}

// Certification is the film's certification in Country: that of its
// first release, by certificationTypes, that has one; "" when none has.
func (m Movie) Certification() string {
	for _, r := range m.ReleaseDates.Results {
		if r.Country != Country {
			continue
		}
		for _, typ := range certificationTypes {
			for _, rel := range r.Releases {
				if rel.Type == typ && rel.Certification != "" {
					return rel.Certification
				}
			}
		}
	}
	return ""
}

// Series is what the server reads of a series' details, with what they
// append.
type Series struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	OriginalName   string  `json:"original_name"`
	Overview       string  `json:"overview"`
	FirstAirDate   string  `json:"first_air_date"`
	EpisodeRunTime []int   `json:"episode_run_time"`
	VoteAverage    float64 `json:"vote_average"`
	VoteCount      int     `json:"vote_count"`
	Genres         []Named `json:"genres"`
	Status         string  `json:"status"`
	InProduction   bool    `json:"in_production"`
	PosterPath     string  `json:"poster_path"`
	BackdropPath   string  `json:"backdrop_path"`
	Images         Images  `json:"images"`
	NextEpisode    *struct {
		Season int `json:"season_number"`
	} `json:"next_episode_to_air"`
	LastEpisode *struct {
		Season int `json:"season_number"`
	} `json:"last_episode_to_air"`
	Seasons []struct {
		Number   int `json:"season_number"`
		Episodes int `json:"episode_count"`
	} `json:"seasons"`
	AlternativeTitles struct {
		Results []Named `json:"results"`
	} `json:"alternative_titles"`
	ContentRatings struct {
		Results []struct {
			Country string `json:"iso_3166_1"`
			Rating  string `json:"rating"`
		} `json:"results"`
	} `json:"content_ratings"`
	ExternalIDs struct {
		ImdbID string `json:"imdb_id"`
		TvdbID int    `json:"tvdb_id"`
	} `json:"external_ids"`
}

// Airing reports whether the series still airs: TMDB says it is in
// production, or names its next episode.
func (s Series) Airing() bool { return s.InProduction || s.NextEpisode != nil }

// Certification is the series' content rating in Country, or "".
func (s Series) Certification() string {
	for _, r := range s.ContentRatings.Results {
		if r.Country == Country {
			return r.Rating
		}
	}
	return ""
}

// HasSeason reports whether TMDB lists a season of that number.
func (s Series) HasSeason(n int) bool {
	for _, se := range s.Seasons {
		if se.Number == n {
			return true
		}
	}
	return false
}

// Season is what the server reads of a season's details.
type Season struct {
	Number     int    `json:"season_number"`
	Name       string `json:"name"`
	Overview   string `json:"overview"`
	AirDate    string `json:"air_date"`
	PosterPath string `json:"poster_path"`
	Episodes   []struct {
		Number      int     `json:"episode_number"`
		Name        string  `json:"name"`
		StillPath   string  `json:"still_path"`
		Overview    string  `json:"overview"`
		AirDate     string  `json:"air_date"`
		Runtime     int     `json:"runtime"`
		VoteAverage float64 `json:"vote_average"`
		VoteCount   int     `json:"vote_count"`
	} `json:"episodes"`
}

// Found is what /find answers: TMDB's films and series for another
// provider's id.
type Found struct {
	Movies []struct {
		ID int `json:"id"`
	} `json:"movie_results"`
	Series []struct {
		ID int `json:"id"`
	} `json:"tv_results"`
}
