package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const htmlTemplate = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Catalog.Title}} - dir2opds</title>
    <style>
        :root {
            --primary-color: #2c3e50;
            --secondary-color: #34495e;
            --accent-color: #3498db;
            --text-color: #333;
            --bg-color: #f4f7f6;
            --card-bg: #fff;
        }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
            background-color: var(--bg-color);
            color: var(--text-color);
            line-height: 1.6;
            margin: 0;
            padding: 0;
        }
        .container {
            max-width: 900px;
            margin: 0 auto;
            padding: 20px;
        }
        header {
            background-color: var(--primary-color);
            color: white;
            padding: 20px 0;
            margin-bottom: 30px;
            box-shadow: 0 2px 5px rgba(0,0,0,0.1);
        }
        header h1 {
            margin: 0;
            text-align: center;
            font-size: 1.5rem;
        }
        .breadcrumb {
            margin-bottom: 20px;
            font-size: 0.9rem;
        }
        .breadcrumb a {
            color: var(--accent-color);
            text-decoration: none;
        }
        .breadcrumb span {
            margin: 0 5px;
            color: #999;
        }
        .search-box {
            margin-bottom: 30px;
            text-align: center;
        }
        .search-box input[type="text"] {
            padding: 10px;
            width: 60%;
            border: 1px solid #ddd;
            border-radius: 4px 0 0 4px;
            outline: none;
        }
        .search-box button {
            padding: 10px 20px;
            background-color: var(--accent-color);
            color: white;
            border: none;
            border-radius: 0 4px 4px 0;
            cursor: pointer;
        }
        .entry-list {
            list-style: none;
            padding: 0;
        }
        .entry-item {
            background-color: var(--card-bg);
            margin-bottom: 15px;
            padding: 15px;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.05);
            display: flex;
            align-items: center;
            transition: transform 0.2s;
        }
        .entry-item:hover {
            transform: translateY(-2px);
            box-shadow: 0 4px 8px rgba(0,0,0,0.1);
        }
        .entry-icon {
            font-size: 2rem;
            margin-right: 20px;
            width: 50px;
            text-align: center;
            flex-shrink: 0;
        }
        .entry-cover {
            width: 60px;
            height: 80px;
            object-fit: cover;
            margin-right: 20px;
            border-radius: 4px;
            box-shadow: 0 1px 3px rgba(0,0,0,0.2);
        }
        .entry-details {
            flex-grow: 1;
        }
        .entry-title {
            font-size: 1.1rem;
            font-weight: bold;
            margin-bottom: 5px;
        }
        .entry-title a {
            color: var(--primary-color);
            text-decoration: none;
        }
        .entry-title a:hover {
            color: var(--accent-color);
        }
        .entry-meta {
            font-size: 0.85rem;
            color: #777;
        }
        .pagination {
            display: flex;
            justify-content: center;
            margin-top: 30px;
            gap: 10px;
        }
        .pagination a {
            padding: 8px 15px;
            background-color: var(--card-bg);
            color: var(--accent-color);
            text-decoration: none;
            border-radius: 4px;
            border: 1px solid #ddd;
        }
        .pagination span.current {
            padding: 8px 15px;
            background-color: var(--accent-color);
            color: white;
            border-radius: 4px;
        }
        .footer {
            margin-top: 50px;
            text-align: center;
            font-size: 0.8rem;
            color: #999;
            padding-bottom: 20px;
        }
    </style>
</head>
<body>
    <header>
        <div class="container">
            <h1>dir2opds</h1>
        </div>
    </header>

    <div class="container">
        {{if .EnableSearch}}
        <div class="search-box">
            <form action="/search" method="get">
                <input type="text" name="q" placeholder="Search books..." value="{{.Query}}">
                <button type="submit">Search</button>
            </form>
        </div>
        {{end}}

        <div class="breadcrumb">
            <a href="/">Home</a>
            {{range .Breadcrumbs}}
                <span>/</span>
                <a href="{{.Path}}">{{.Name}}</a>
            {{end}}
        </div>

        <ul class="entry-list">
            {{range .Entries}}
            <li class="entry-item">
                {{if .CoverURL}}
                <img src="{{.CoverURL}}" class="entry-cover" alt="Cover">
                {{else}}
                <div class="entry-icon">
                    {{if eq .Type 0}}📄{{else}}📁{{end}}
                </div>
                {{end}}
                <div class="entry-details">
                    <div class="entry-title">
                        <a href="{{.Href}}">{{if .Title}}{{.Title}}{{else}}{{.Name}}{{end}}</a>
                    </div>
                    <div class="entry-meta">
                        {{if .Author}}By {{.Author}} | {{end}}
                        {{if .Size}}{{.SizeDisplay}} | {{end}}
                        {{if .ModTimeDisplay}}Modified: {{.ModTimeDisplay}}{{end}}
                    </div>
                </div>
            </li>
            {{else}}
            <p>No entries found.</p>
            {{end}}
        </ul>

        {{if gt .TotalPages 1}}
        <div class="pagination">
            {{if gt .CurrentPage 1}}
            <a href="{{.PrevPageURL}}">&laquo; Previous</a>
            {{end}}
            <span class="current">Page {{.CurrentPage}} of {{.TotalPages}}</span>
            {{if lt .CurrentPage .TotalPages}}
            <a href="{{.NextPageURL}}">Next &raquo;</a>
            {{end}}
        </div>
        {{end}}

        <div class="footer">
            Generated by <a href="https://github.com/SamLaio/dir2opds" style="color: #999;">dir2opds</a>
        </div>
    </div>
</body>
</html>
`

type Breadcrumb struct {
	Name string
	Path string
}

type HTMLEntry struct {
	CatalogEntry
	Href           string
	CoverURL       string
	SizeDisplay    string
	ModTimeDisplay string
}

type HTMLData struct {
	Catalog      *Catalog
	Entries      []HTMLEntry
	Breadcrumbs  []Breadcrumb
	EnableSearch bool
	Query        string
	CurrentPage  int
	TotalPages   int
	PrevPageURL  string
	NextPageURL  string
}

type staticXMLFeed struct {
	Title   string           `xml:"title"`
	Links   []staticXMLLink  `xml:"link"`
	Entries []staticXMLEntry `xml:"entry"`
}

type staticXMLEntry struct {
	Title  string          `xml:"title"`
	Author *staticXMLText  `xml:"author"`
	Links  []staticXMLLink `xml:"link"`
}

type staticXMLText struct {
	Name string `xml:"name"`
}

type staticXMLLink struct {
	Rel   string `xml:"rel,attr"`
	Title string `xml:"title,attr"`
	Href  string `xml:"href,attr"`
	Type  string `xml:"type,attr"`
}

func (s OPDS) renderHTML(w http.ResponseWriter, req *http.Request, catalog *Catalog) error {
	tmpl, err := template.New("catalog").Parse(htmlTemplate)
	if err != nil {
		return err
	}

	data := HTMLData{
		Catalog:      catalog,
		EnableSearch: s.EnableSearch,
		Query:        req.URL.Query().Get("q"),
		CurrentPage:  catalog.Page,
		TotalPages:   (catalog.Total + catalog.PageSize - 1) / catalog.PageSize,
	}

	data.Breadcrumbs = htmlBreadcrumbs(req)

	// Entries
	for _, entry := range catalog.Entries {
		var entryPath string
		if entry.Href != "" {
			entryPath = entry.Href
		} else if strings.HasPrefix(catalog.ID, "search:") {
			entryPath = "/" + entry.Name
		} else {
			entryPath = path.Join(req.URL.Path, entry.Name)
		}

		href := entryPath
		if entry.Href == "" {
			href = (&url.URL{Path: entryPath}).String()
		}

		var coverURL string
		if s.ExtractMetadata && entry.CoverPath != "" && entry.Type == pathTypeFile {
			coverURL = "/cover?file=" + url.QueryEscape(entryPath)
		}

		var modTimeDisplay string
		if !entry.ModTime.IsZero() {
			modTimeDisplay = entry.ModTime.Format("2006-01-02")
		}

		data.Entries = append(data.Entries, HTMLEntry{
			CatalogEntry:   entry,
			Href:           href,
			CoverURL:       coverURL,
			SizeDisplay:    formatSize(entry.Size),
			ModTimeDisplay: modTimeDisplay,
		})
	}

	// Pagination
	if data.CurrentPage > 1 {
		data.PrevPageURL = buildPageURL(req.URL.Path, req.URL.Query(), data.CurrentPage-1)
	}
	if data.CurrentPage < data.TotalPages {
		data.NextPageURL = buildPageURL(req.URL.Path, req.URL.Query(), data.CurrentPage+1)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.Execute(w, data)
}

func (s OPDS) renderHTMLFromXML(w http.ResponseWriter, req *http.Request, content []byte) error {
	var feed staticXMLFeed
	decoder := xml.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&feed); err != nil {
		return err
	}

	page := parsePage(req.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	catalog := &Catalog{
		ID:       req.URL.Path,
		Title:    feed.Title,
		Page:     page,
		PageSize: len(feed.Entries),
		Total:    len(feed.Entries),
	}
	data := HTMLData{
		Catalog:      catalog,
		EnableSearch: s.EnableSearch,
		Query:        req.URL.Query().Get("q"),
		CurrentPage:  page,
		TotalPages:   page,
	}

	for _, link := range feed.Links {
		switch link.Rel {
		case "previous":
			data.PrevPageURL = link.Href
		case "next":
			data.NextPageURL = link.Href
			if data.TotalPages <= page {
				data.TotalPages = page + 1
			}
		case "last":
			if lastPage := pageFromURL(link.Href); lastPage > 0 {
				data.TotalPages = lastPage
			}
		}
	}

	data.Breadcrumbs = htmlBreadcrumbs(req)

	for _, entry := range feed.Entries {
		htmlEntry := HTMLEntry{
			CatalogEntry: CatalogEntry{
				Name:  entry.Title,
				Title: entry.Title,
			},
			Href: "#",
		}
		if entry.Author != nil {
			htmlEntry.Author = entry.Author.Name
		}
		for _, link := range entry.Links {
			href := s.htmlHref(link.Href)
			switch {
			case link.Rel == "subsection":
				htmlEntry.Type = pathTypeDirOfFiles
				htmlEntry.Href = href
				if link.Title != "" {
					htmlEntry.Name = link.Title
				}
			case strings.Contains(link.Rel, "image"):
				if htmlEntry.CoverURL == "" {
					htmlEntry.CoverURL = href
				}
			case link.Rel == "http://opds-spec.org/acquisition":
				htmlEntry.Type = pathTypeFile
				htmlEntry.Href = href
				if link.Title != "" {
					htmlEntry.Name = link.Title
				}
			}
		}
		data.Entries = append(data.Entries, htmlEntry)
	}

	tmpl, err := template.New("catalog").Parse(htmlTemplate)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return tmpl.Execute(w, data)
}

func (s OPDS) htmlHref(href string) string {
	if s.BaseURL == "" || !strings.HasPrefix(href, s.BaseURL) {
		return href
	}
	trimmed := strings.TrimPrefix(href, strings.TrimSuffix(s.BaseURL, "/"))
	if trimmed == "" {
		return "/"
	}
	return trimmed
}

func htmlBreadcrumbs(req *http.Request) []Breadcrumb {
	var breadcrumbs []Breadcrumb
	urlPath := strings.Trim(req.URL.Path, "/")
	if urlPath != "" {
		parts := strings.Split(urlPath, "/")
		current := ""
		for _, part := range parts {
			current += "/" + part
			breadcrumbs = append(breadcrumbs, Breadcrumb{
				Name: part,
				Path: current,
			})
		}
	}

	query := req.URL.Query()
	switch query.Get("sort") {
	case "name":
		breadcrumbs = append(breadcrumbs, Breadcrumb{
			Name: "By Name",
			Path: buildSortURL(req.URL.Path, query, "name"),
		})
	case "date":
		breadcrumbs = append(breadcrumbs, Breadcrumb{
			Name: "By Date Added",
			Path: buildSortURL(req.URL.Path, query, "date"),
		})
	case "type":
		fileType := normalizeBookType(query.Get("type"))
		breadcrumbs = append(breadcrumbs, Breadcrumb{
			Name: "By Type",
			Path: buildSortURL(req.URL.Path, query, "type"),
		})
		if fileType != "" {
			breadcrumbs = append(breadcrumbs, Breadcrumb{
				Name: bookTypeTitle(fileType),
				Path: buildTypeURL(req.URL.Path, req.URL.Query(), fileType),
			})
		}
	}

	return breadcrumbs
}

func pageFromURL(rawURL string) int {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return 0
	}
	page, err := strconv.Atoi(parsed.Query().Get("page"))
	if err != nil {
		return 0
	}
	return page
}

func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}
