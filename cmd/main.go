package main

import (
	"bufio"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type Template struct {
	tmpl *template.Template
}

func newTemplate() *Template {
	return &Template{
		tmpl: template.Must(template.ParseGlob("views/*.html")),
	}
}

func (t *Template) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.tmpl.ExecuteTemplate(w, name, data)
}

func main() {
	e := echo.New()

	// middlewares
	e.Static("/static", "static")
	e.Use(middleware.Recover())
	e.Use(middleware.Logger())
	e.Use(middleware.CORS())
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{
		Level: 5,
	}))

	// templates
	e.Renderer = newTemplate()

	// routes
	e.GET("/", HandlerGETIndex)
	e.POST("/download", HandlerPOSTDownload)
	e.Any("/status", HandlerGETStatus)

	e.Any("/404", HandlerNotFound)
	e.Any("*", HandlerNotFound)

	e.Logger.Fatal(e.Start(":3000"))
}

type PageBaseMeta struct {
	Title string
}

type Download struct {
	Url             string
	Status          string
	SourceFile      string
	Id              string
	TotalFiles      int
	DownloadedFiles int
	TSFiles         []string
}

var responses = map[string]Download{}

func CreatePageBaseMeta(title string) PageBaseMeta {
	return PageBaseMeta{
		Title: title,
	}
}

func HandlerNotFound(c echo.Context) error {
	metadata := CreatePageBaseMeta("404")
	return c.Render(404, "404", metadata)
}

func HandlerGETIndex(c echo.Context) error {
	metadata := CreatePageBaseMeta("Downloader")
	fmt.Println(responses)
	return c.Render(200, "index", metadata)
}

type DownloadStatus = string

const (
	READY_TO_DOWNLOAD DownloadStatus = "READY_TO_DOWNLOAD"
	DOWNLOADING       DownloadStatus = "DOWNLOADING"
	DOWNLOADED        DownloadStatus = "DOWNLOADED"
)

func HandlerPOSTDownload(c echo.Context) error {
	url := c.FormValue("url")
	if url == "" {
		return c.String(http.StatusBadRequest, "No URL provided")
	}

	fileToDownload, err := http.Get(url)
	if err != nil {
		fmt.Println(err)
		return c.String(http.StatusInternalServerError, "Error downloading file")
	}
	defer fileToDownload.Body.Close()

	fmt.Printf("Status: %s", fileToDownload.Status)

	folderName := uuid.New().String()
	err = os.Mkdir("downloads/"+folderName, 0755)
	if err != nil {
		return c.String(http.StatusInternalServerError, "Error creating folder")
	}
	out, err := os.Create("downloads/" + folderName + "/main.m3u8")
	if err != nil {
		return c.String(http.StatusInternalServerError, "Error creating file")
	}
	defer out.Close()

	_, err = io.Copy(out, fileToDownload.Body)
	if err != nil {
		return c.String(http.StatusInternalServerError, "Error copying file")
	}

	download := Download{
		Url:        url,
		Status:     READY_TO_DOWNLOAD,
		SourceFile: "main.m3u8",
		Id:         folderName,
	}

	responses[folderName] = download

	c.Response().Header().Set("Location", "/status?download="+folderName)

	return c.String(http.StatusPermanentRedirect, "Ready to download\n")
}

func HandlerGETStatus(c echo.Context) error {
	download := c.QueryParam("download")
	if download == "" {
		return c.String(http.StatusBadRequest, "No download provided")
	}

	response := responses[download]

	if response.Url == "" {
		c.Response().Header().Set("Location", "/404")
		return c.String(http.StatusPermanentRedirect, "Not Found")
	}

	// read file
	file, err := os.Open("downloads/" + download + "/main.m3u8")
	if err != nil {
		return c.String(http.StatusInternalServerError, "Error reading file")
	}
	defer file.Close()

	fileScanner := bufio.NewScanner(file)

	fileScanner.Split(bufio.ScanLines)

	var count uint16 = 0

	for fileScanner.Scan() {
		text := fileScanner.Text()
		if len(text) >= 14 && text[:14] == "#EXT-X-VERSION" {
			version := text[15:]
			if version != "3" {
				fmt.Println("Error with m3u8 version" + text + " Version: " + version)
				return c.String(http.StatusInternalServerError, "Error with m3u8 version")
			}
		}

		if text == "#EXT-X-ENDLIST" {
			break
		}

		if len(text) >= 7 && text[:7] == "#EXTINF" {
			count++
		}
	}

	file.Seek(0, 0)

	fileList := make([]string, count, count)

	i := 0
	for fileScanner.Scan() {
		text := fileScanner.Text()
		fileScanner.Scan()
		if len(text) >= 7 && text[:7] == "#EXTINF" {
			// check if not empty
			nextLine := fileScanner.Text()
			if nextLine == "" {
				continue
			}
			fileList[i] = nextLine
			i++
		}
	}

	response.TotalFiles = int(count)
	response.TSFiles = fileList

	for i, fileString := range fileList {
		if fileString == "" {
			continue
		}

		var requestUrl string

		fmt.Println("fileString " + fileString)
		if fileString[:4] == "http" || fileString[:5] == "https" {
			requestUrl = fileString
		} else {
			requestUrl = response.Url + "/" + fileString
		}

		response.Status = DOWNLOADING
		fmt.Printf("Downloading: #%d %s\n", i, requestUrl)
		fileToDownload, err := http.Get(requestUrl)
		if err != nil {
			fmt.Println(err)
			return c.String(http.StatusInternalServerError, "Error downloading chunks")
		}
		defer fileToDownload.Body.Close()

		out, err := os.Create("downloads/" + response.Id + "/" + fmt.Sprintf("%d", i) + ".ts")
		if err != nil {
			return c.String(http.StatusInternalServerError, "Error creating chunk file")
		}
		defer out.Close()

		_, err = io.Copy(out, fileToDownload.Body)
		if err != nil {
			return c.String(http.StatusInternalServerError, "Error copying chunk file")
		}

		response.DownloadedFiles = i + 1
	}

	response.Status = DOWNLOADED

	return c.JSON(http.StatusOK, response)
}
