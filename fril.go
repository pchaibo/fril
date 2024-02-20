package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"utils"

	"github.com/PuerkitoBio/goquery"
	colly "github.com/gocolly/colly/v2"
	"github.com/gocolly/colly/v2/proxy"
	"github.com/gocolly/colly/v2/queue"
	"github.com/gocolly/redisstorage"
)

type Frilconf struct {
	Getcatjson      string
	CategoryUrl     string
	SearchUrl       string
	ItemUrl         string
	Thread          int
	PerSiteNum      int
	DataDir         string
	PriceMin        int
	PriceMax        int
	Delay           time.Duration
	RequestTimeout  time.Duration
	Useproxy        int
	Average         int
	ExcludeCategory []string
}

type Category struct {
	Id        int    `json:"id"`
	Parent_id int    `json:"parent_id"`
	Name      string `json:"name"`
	Sort      int    `sort:"sort"`
	Children  []struct {
		Id        int    `json:"id"`
		Parent_id int    `json:"parent_id"`
		Name      string `json:"name"`
		Sort      int    `sort:"sort"`
		Children  []struct {
			Id        int    `json:"id"`
			Parent_id int    `json:"parent_id"`
			Name      string `json:"name"`
			Sort      int    `sort:"sort"`
		}
	}
}

//	type itemlink struct {
//		link string
//	}
type Pros struct {
	Imagesurl string
	Name      string
	Category  []int
	Price     string
}

var lock sync.Mutex

func TextRecord(filename string) []string {
	// 2.rand ua
	file, err := os.ReadFile(filename)
	if err != nil {
		log.Panicf("open txtfile error %v", err)
	}
	return regexp.MustCompile("\r\n|\n").Split(string(file), -1)
}
func getSiteTxtFilename() string {
	filename := utils.RandomString(6)
	if utils.FileExist(filename) {
		filename = getSiteTxtFilename()
	}
	return filename
}
func getConf() Frilconf {
	conf := Frilconf{}
	mercariconf := "fril.conf"
	if utils.FileExist(mercariconf) {
		content, err := os.ReadFile(mercariconf)
		if err != nil {
			log.Printf("read %s error %v", mercariconf, err)
		}
		if err := json.Unmarshal(content, &conf); err != nil {
			log.Printf("%s unmarshal error %v", mercariconf, err)
		}
	} else {
		panic("mercari.conf not found")
	}
	return conf
}
func main() {
	conf := getConf()
	max := conf.PriceMax
	min := conf.PriceMin
	//Thread := conf.Thread
	var Thread int
	caturl := conf.CategoryUrl
	Getcatjson := conf.Getcatjson
	DataDir := conf.DataDir
	PERSITECOUNT := conf.PerSiteNum
	Useproxy := conf.Useproxy
	ItemCnt := 0
	var LIMIT int = 2e2
	currCount := 0
	ErrCnt := 0
	products := make([]string, 0, LIMIT)
	filename := fmt.Sprintf("%s/%s.txt", DataDir, getSiteTxtFilename())
	//----log file.
	logFile, logErr := os.OpenFile("fril.log", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if logErr != nil {
		fmt.Println("Fail to find", *logFile, "cServer start Failed")
		os.Exit(1)
	}
	defer logFile.Close()

	// log.SetOutput(logFile)
	// log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	writers := []io.Writer{
		logFile,
		os.Stdout}
	fileAndStdoutWriter := io.MultiWriter(writers...)
	log := log.New(fileAndStdoutWriter, "", log.Ldate|log.Ltime|log.Lshortfile)
	//end
	start := time.Now()
	log.Printf("begin %s", start.Local())
	c := colly.NewCollector(
		colly.AllowURLRevisit(),
		colly.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Safari/537.36"),
	)

	c.Limit(&colly.LimitRule{
		Delay: conf.Delay * time.Millisecond,
	})
	c.SetRequestTimeout(conf.RequestTimeout * time.Second)
	c.WithTransport(&http.Transport{
		DisableKeepAlives: true,
	})

	proxyRecords := TextRecord("proxy.txt")
	pronum := len(proxyRecords)
	Thread = pronum / conf.Average
	if Useproxy > 0 {
		rp, err := proxy.RoundRobinProxySwitcher(proxyRecords...)
		if err != nil {
			log.Fatal(err)
		}
		c.SetProxyFunc(rp)
	}

	catecollector := c.Clone()
	itemlist := c.Clone() //取列表url
	productrun := c.Clone()
	//redisStorage
	// 详细页
	storageq := &redisstorage.Storage{
		Address:  "127.0.0.1:6379",
		Password: "",
		DB:       5,
		Prefix:   "category",
	}
	productrun.SetStorage(storageq)
	q, _ := queue.New(
		Thread, // Number of consumer threads
		storageq,
	)
	//列表
	storagk := &redisstorage.Storage{
		Address:  "127.0.0.1:6379",
		Password: "",
		DB:       5,
		Prefix:   "frilist",
	}
	itemlist.SetStorage(storagk)
	qi, _ := queue.New(
		Thread, // Number of consumer threads
		storagk,
		//&queue.InMemoryQueueStorage{MaxSize: 5000000}, // Use default queue storage
	)

	storage3 := &redisstorage.Storage{
		Address:  "127.0.0.1:6379",
		Password: "",
		DB:       5,
		Prefix:   "fripage",
	}
	catecollector.SetStorage(storage3)
	q3, _ := queue.New(
		Thread, // Number of consumer threads
		storage3,
	)

	//取商品信息
	productrun.OnError(func(r *colly.Response, err error) {
		lock.Lock()
		ErrCnt++
		lock.Unlock()
	})
	productrun.OnRequest(func(r *colly.Request) {
		ItemCnt--
	})
	productrun.OnHTML("div.container.new-rakuma", func(h *colly.HTMLElement) {
		fmt.Println(h.Request.URL)
		dom, err := goquery.NewDocumentFromReader(strings.NewReader(string(h.Response.Body)))
		if err != nil {
			log.Fatal("query html error")
			return
		}

		u, _ := url.Parse(h.Request.URL.String())
		purl := strings.Split(u.Path, "/")
		if len(purl) < 1 || len(purl[1]) < 1 {
			log.Println("purl no null id:", h.Request.URL.String())
			return
		}

		var srclink []string
		src, _ := dom.Find("#photoFrame").Find("img").Attr("src")
		if len(src) > 10 {
			srclink = append(srclink, src)
		}
		h.ForEach("div.sp-slide", func(i int, h *colly.HTMLElement) {
			imgsrc := h.ChildAttr("img", "src")
			srclink = append(srclink, imgsrc)
		})

		name := h.ChildText("h1.item__name")
		if name == "" {
			log.Println("title no null", h.Request.URL.String())
			return
		}
		if len(srclink) < 1 {
			log.Println("srclink no null", h.Request.URL.String())
			return
		}
		//item__price
		prciesstr := h.ChildText("p.item__value_area >span.item__price")
		if prciesstr == "" {
			log.Println("prciesstr no null", h.Request.URL.String())
			return
		}
		//prcies := strings.Trim(prciesstr, "￥")//¥ string([]rune(str)[:4])
		prcies := string([]rune(prciesstr)[1 : len(prciesstr)-1])
		//fmt.Println("prcies: ", prcies)
		prciek := strings.Replace(prcies, ",", "", -1)
		prcie, err := strconv.ParseFloat(prciek, 64)
		if err != nil {
			fmt.Println("prcie no ull")
			prcie = 0.0
			return
		}
		//分类
		categors := make([]string, 0, 3)
		h.ForEach("div.item-status >table.item__details ", func(i int, h *colly.HTMLElement) {
			categors = h.ChildTexts("a")
		})
		strAttr := ""
		info := `<tr><td>カテゴリー</td><td>` + strings.Join(categors, ">") + `</td></tr>`
		th1 := dom.Find(".item-status .item__details").Find("tr").Find("th").Eq(1).Text()
		td1 := dom.Find(".item-status .item__details").Find("tr").Find("td").Eq(1).Text()
		th2 := dom.Find(".item-status .item__details").Find("tr").Find("th").Eq(2).Text()
		td2 := dom.Find(".item-status .item__details").Find("tr").Find("td").Eq(2).Text()
		th3 := dom.Find(".item-status .item__details").Find("tr").Find("th").Eq(3).Text()
		td3 := dom.Find(".item-status .item__details").Find("tr").Find("td").Eq(3).Text()
		th1 = strings.TrimSpace(th1)
		//fmt.Println("pr:", th1, td1)
		//サイズ
		if strings.Contains(th1, "サイズ") {
			info += `<tr><td>商品のサイズ</td><td>` + td1 + `</td></tr>`
			strAttr += `商品のサイズ===` + td1
		}

		if th2 == "ブランド" && len(td2) > 0 {
			info += `<tr><td>ブランド</td><td>` + td2 + `</td></tr>`
		}

		if strings.Contains(th3, "状態") {
			info += `<tr><td>商品の状態</td><td>` + td3 + `</td></tr>`
		}
		//item__description__line-limited
		//Description := h.ChildText("div.hidden-xs >div.item__description >p")
		//hidden-xs
		Description := h.ChildText("div.hidden-xs >div.item__description.only__pc >div.item__description__line-limited")
		Description = strings.NewReplacer([]string{"\r\n", "<br>", "\r", "<br>", "\n", "<br>"}...).Replace(Description)
		Description = fmt.Sprintf(`<div><h2>商品の説明</h2>%s</div><br><h2>商品の情報</h2><table>%s</table>`, Description, info)
		//create product line
		line := make([]string, 0, 7)
		line = append(line, purl[1])
		line = append(line, name)
		line = append(line, fmt.Sprintf("%.2f", prcie))
		strcate := strings.Join(categors, "|||")
		line = append(line, strcate)
		if len(strAttr) == 0 {
			strAttr = "None"
		}
		line = append(line, "None")
		line = append(line, strings.Join(srclink, "|||"))
		line = append(line, Description)
		//fmt.Println(line)
		lock.Lock()
		currCount++
		products = append(products, strings.NewReplacer([]string{"\r\n", "", "\r", "", "\n", ""}...).Replace(strings.Join(line, "[##]")))
		//fmt.Println("line:", products)

		if len(products) == LIMIT || currCount%PERSITECOUNT == 0 {
			if err := utils.WriteFile(filename, strings.Join(products, "[line]\n")+"[line]\n"); err != nil {
				log.Printf("write ids to file error %s", err)
			}
			n, err := q.Size()
			if err != nil {
				log.Printf("q.size() error : %v", err)
			}

			log.Printf("queue: %d  %d  goroutines: %d		+ok%d+err=%d", n, qi.Threads, runtime.NumGoroutine(), currCount, ErrCnt)
			products = make([]string, 0, LIMIT) //
			if currCount%PERSITECOUNT == 0 {    // new filename
				log.Printf("finish file: %s.txt", filename)
				filename = fmt.Sprintf("%s/%s.txt", DataDir, getSiteTxtFilename()) //
				if ItemCnt < PERSITECOUNT {
					filename = filename + ".last"
				}

				if err := utils.PathExists(filename); err != nil {
					log.Printf("crate site data file dir error %s", err)
				}
			}

		}

		lock.Unlock()
	})
	//取列表url
	itemlist.OnHTML("section.view.view_grid", func(h *colly.HTMLElement) {
		//fmt.Println(h.Request.URL)
		h.ForEach("div.item-box", func(i int, h *colly.HTMLElement) {
			href := h.ChildAttr("div.item-box__image-wrapper >a.link_category_image", "href")
			//fmt.Println(href)
			//下架商品
			SOLD := h.ChildText("div.item-box__image-wrapper >a.link_category_image >div.item-box__soldout_ribbon")
			if SOLD != "SOLD OUT" {
				//fmt.Println("sold: ", SOLD)
				q.AddURL(href)
			}

		})
	})

	itemlist.OnError(func(r *colly.Response, err error) {
		//log.Println("itemlist error:", r.Request.URL)
	})

	//生成分页
	catecollector.OnError(func(r *colly.Response, err error) {
		log.Println("page url err  ", r.StatusCode, r.Request.URL.String())
		log.Println("err:", err)
	})
	catecollector.OnHTML("div.item-header", func(r *colly.HTMLElement) {
		//fmt.Println(r.Request.URL)
		numstr := r.ChildText("div.col-sm-12.col-xs-12.page-count.text-left")
		if len(numstr) < 1 {
			//fmt.Println("numstr len null")
			log.Println("numstr len null ", r.Request.URL)
			return
		}
		num := regexp.MustCompile("([1-9].*)件中").FindStringSubmatch(numstr)
		if len(num) < 1 {
			//fmt.Println("num null")
			//log.Println("page count noll ", r.Request.URL)
			return
		}
		if len(num[1]) < 1 {
			fmt.Println("num error:")
			return
		}
		reg := regexp.MustCompile("\\,")
		res := ""
		s := reg.ReplaceAll([]byte(num[1]), []byte(res))
		count, err := strconv.Atoi(string(s))
		//fmt.Println("count:", count)
		if err != nil {
			fmt.Println("count error")
			return
		}
		if count < 1 {
			log.Println("count < 1")
			return
		}
		u, err := url.Parse(r.Request.URL.RequestURI())
		if err != nil {
			fmt.Println("ulr:", err)
			return
		}
		u.Scheme = r.Request.URL.Scheme
		u.Host = r.Request.URL.Host
		t := u.Query()
		oldmin, _ := strconv.Atoi(t.Get("min"))
		oldmax, _ := strconv.Atoi(t.Get("max"))
		if count > 3600 && oldmin != oldmax && (oldmax-oldmin) > 3000 {
			lock.Lock()
			minend := (oldmax + oldmin) / 2
			setmax := strconv.Itoa(minend)
			t.Set("max", setmax)
			u.RawQuery = t.Encode()
			murl := u.String()
			q3.AddURL(murl)
			//r.Request.Visit(murl)
			lock.Unlock()

			lock.Lock()
			maxstart := minend + 1000 //price
			t.Set("min", strconv.Itoa(maxstart))
			t.Set("max", strconv.Itoa(oldmax))
			u.RawQuery = t.Encode()
			maxurl := u.String()
			q3.AddURL(maxurl)
			//r.Request.Visit(maxurl)
			lock.Unlock()
			return

		} else {
			page := count / 36
			if (count % 36) != 0 {
				page = page + 1
			}
			if page > 100 {
				page = 100
			}
			k := u.Query()
			geturl := u.Scheme + "://" + u.Host + u.Path + "/page/%d?min=%s&max=%s"
			//fmt.Println(geturl, "url:", u.Scheme+"://"+u.Host)
			for i := 1; i < page+1; i++ {
				res := fmt.Sprintf(geturl, i, k.Get("min"), k.Get("max"))
				qi.AddURL(res)

			}
		}

	})

	c.OnResponse(func(r *colly.Response) {
		code := r.StatusCode
		if code != 200 {
			log.Println("分类调取出错:")
			return

		}
		var cat []Category
		if err := json.Unmarshal(r.Body, &cat); err != nil {
			log.Println("reponsetext to json error:", err)
		}
		//fmt.Println("catea:", cat)

		for _, vcat := range cat {
			//fmt.Println(vcat.Name) //过滤
			if utils.Contains(conf.ExcludeCategory, vcat.Name) {
				continue
			}
			for _, vcat2 := range vcat.Children {
				//categoryid = append(categoryid, vcat2.Id)
				for _, vcat3 := range vcat2.Children {
					//q3.AddURL(fmt.Sprintf(caturl, vcat3.Id, vcat3.Id, min, max))
					addurl := fmt.Sprintf(caturl, vcat3.Id, vcat3.Id, min, max)
					//fmt.Println("url:", addurl)
					q3.AddURL(addurl)
				}
			}
		}

	})
	c.OnError(func(r *colly.Response, err error) {
		log.Println("open url error", r.Request.URL, err)
	})
	fmt.Println(Getcatjson)
	//c.Visit(Getcatjson)
	//q3.Run(catecollector)
	//qi.Run(itemlist)

	ItemLen, err := q.Size()
	if err != nil {
		log.Printf("qi.size itemlen error %v", err)
	}
	ItemCnt = ItemLen
	//q.Run(productrun)
	log.Printf("end item detail %s", time.Since(start))
	// url := "https://fril.jp/category/1621?category_id=1621max=1000000min=502501"
	// itemlist.Visit(url)
	// url := "https://fril.jp/category/1258/page/4"
	// itemlist.Visit(url)
	productrun.Visit("https://item.fril.jp/ba0b3c0a79fd5ba7e0e60e385f91ff1e")
}
