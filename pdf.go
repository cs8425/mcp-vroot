package main

import (
	"bytes"
	"fmt"
	"image/png"
	"io"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/klippa-app/go-pdfium/webassembly"
)

var (
	pdfPool pdfium.Pool
)

func init() {
	t0 := time.Now()
	Vln(4, "[pdf]init start", t0)
	var err error
	// Init the PDFium library and return the instance to open documents.
	// You can tweak these configs to your need. Be aware that workers can use quite some memory.
	pdfPool, err = webassembly.Init(webassembly.Config{
		MinIdle:  2, // Makes sure that at least x workers are always available
		MaxIdle:  4, // Makes sure that at most x workers are ever available
		MaxTotal: 4, // The maximum number of workers in total, allows the number of workers to grow when needed, items between total max and idle max are automatically cleaned up, while idle workers are kept alive so they can be used directly.
	})
	if err != nil {
		log.Fatal(err)
	}
	Vln(4, "[pdf]init end", pdfPool, time.Since(t0))
}

func openPdf(root *os.Root, fp string) (pdfium.Pdfium, *responses.OpenDocument, func(), error) {
	closeQueue := make([]func() error, 0, 3)
	closeFn := func() {
		slices.Reverse(closeQueue)
		for _, fn := range closeQueue {
			fn()
		}
	}
	fd, sz, err := openFd(root, fp)
	if err != nil {
		return nil, nil, nil, err
	}
	closeQueue = append(closeQueue, fd.Close)
	// defer fd.Close()

	instance, err := pdfPool.GetInstance(time.Second * 30)
	if err != nil {
		closeFn()
		return nil, nil, nil, err
	}
	closeQueue = append(closeQueue, instance.Close)
	// defer instance.Close()

	// Open the PDF using PDFium (and claim a worker)
	doc, err := instance.OpenDocument(&requests.OpenDocument{
		FileReader:     fd,
		FileReaderSize: sz,
	})
	if err != nil {
		closeFn()
		return nil, nil, nil, err
	}

	// Always close the document, this will release its resources.
	closeQueue = append(closeQueue, func() error {
		_, err := instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{
			Document: doc.Document,
		})
		return err
	})
	return instance, doc, closeFn, nil
}

func readPdfToc(root *os.Root, fp string, sb *strings.Builder) error {
	instance, doc, clsFn, err := openPdf(root, fp)
	if err != nil {
		return err
	}
	defer clsFn()

	bookmarks, err := instance.GetBookmarks(&requests.GetBookmarks{Document: doc.Document})
	if err != nil {
		return err
	}

	if sb == nil {
		sb = &strings.Builder{}
	}
	var printBk func(bks []responses.GetBookmarksBookmark, depth int, sb *strings.Builder)
	printBk = func(bks []responses.GetBookmarksBookmark, depth int, sb *strings.Builder) {
		for _, bk := range bks {
			if bk.DestInfo == nil {
				fmt.Fprintf(sb, "%v- %v\n", strings.Repeat("\t", depth), bk.Title)
			} else {
				fmt.Fprintf(sb, "%v- %v (p.%v)\n", strings.Repeat("\t", depth), bk.Title, bk.DestInfo.PageIndex+1)
			}
			if bk.Children != nil {
				printBk(bk.Children, depth+1, sb)
			}
		}
	}
	printBk(bookmarks.Bookmarks, 0, sb)
	return nil
}

func readPdfAsImg(root *os.Root, fp string, w io.Writer, page int, limit int) (int, error) {
	instance, doc, clsFn, err := openPdf(root, fp)
	if err != nil {
		return -1, err
	}
	defer clsFn()

	pageCount, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return -1, err
	}

	// Render the page in DPI 200.
	pageRender, err := instance.RenderPageInDPI(&requests.RenderPageInDPI{
		DPI: 200, // The DPI to render the page in.
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{
				Document: doc.Document,
				Index:    page,
			},
		}, // The page to render, 0-indexed.
	})
	if err != nil {
		return pageCount.PageCount, err
	}
	defer pageRender.Cleanup()
	if w == nil {
		w = &bytes.Buffer{}
	}
	if err := png.Encode(w, pageRender.Result.Image); err != nil {
		return pageCount.PageCount, err
	}
	return pageCount.PageCount, nil
}

func readPdfAsText(root *os.Root, fp string, sb *strings.Builder, page int, limit int) (int, error) {
	instance, doc, clsFn, err := openPdf(root, fp)
	if err != nil {
		return -1, err
	}
	defer clsFn()

	pageCount, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return -1, err
	}

	ret, err := instance.GetPageTextStructured(&requests.GetPageTextStructured{
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{
				Document: doc.Document,
				Index:    page,
			},
		}, // The page to render, 0-indexed.
		Mode: requests.GetPageTextStructuredModeBoth,
		PixelPositions: requests.GetPageTextStructuredPixelPositions{
			Document:  doc.Document,
			Calculate: true,
			DPI:       200,
		},
	})
	if err != nil {
		return pageCount.PageCount, err
	}
	if sb == nil {
		sb = &strings.Builder{}
	}
	if ret.Rects != nil {
		fmt.Fprintf(sb, "<pos_$left,$top-$right,$bottom>:$text\n\n")
		for _, rect := range ret.Rects {
			if rect.PixelPosition == nil {
				sb.WriteString(rect.Text)
				continue
			}
			fmt.Fprintf(sb, "<pos_%v,%v-%v,%v>:%v\n",
				rect.PixelPosition.Left, rect.PixelPosition.Top,
				rect.PixelPosition.Right, rect.PixelPosition.Bottom,
				rect.Text,
			)
		}
	}

	// pageText, err := instance.GetPageText(&requests.GetPageText{
	// 	Page: requests.Page{
	// 		ByIndex: &requests.PageByIndex{
	// 			Document: doc.Document,
	// 			Index:    page,
	// 		},
	// 	}, // The page to render, 0-indexed.
	// })
	// if err != nil {
	// 	return pageCount.PageCount, err
	// }
	// if sb == nil {
	// 	sb = &strings.Builder{}
	// }
	// sb.WriteString(pageText.Text)
	return pageCount.PageCount, nil
}
