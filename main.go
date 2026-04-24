package main

import (
	"bufio"
	"encoding/csv"
	"io"
	"log"
	"net"
	"os"
	"strings"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

func main() {
	srcFile := "ipinfo_lite.csv"
	dstFile := "Country.mmdb"

	writer, err := mmdbwriter.New(
		mmdbwriter.Options{
			DatabaseType: "GeoIP2-Country",
			RecordSize:   24,
		},
	)
	if err != nil {
		log.Fatalf("创建 writer 失败: %v", err)
	}

	fh, err := os.Open(srcFile)
	if err != nil {
		log.Fatalf("无法打开文件 %s: %v", srcFile, err)
	}
	defer fh.Close()

	reader := csv.NewReader(bufio.NewReader(fh))

	if _, err := reader.Read(); err != nil {
		log.Fatalf("读取表头失败: %v", err)
	}

	count := 0
	log.Println("开始处理数据...")

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("读取行错误: %v", err)
			continue
		}

		networkStr := record[0]
		countryCode := record[2]

		if !strings.Contains(networkStr, "/") {
			ip := net.ParseIP(networkStr)
			if ip != nil {
				if ip.To4() != nil {
					networkStr += "/32"
				} else {
					networkStr += "/128"
				}
			}
		}

		_, ipNet, err := net.ParseCIDR(networkStr)
		if err != nil {
			log.Printf("无效的 IP/CIDR 格式，跳过: %s", record[0])
			continue 
		}

		data := mmdbtype.Map{
			"country": mmdbtype.Map{
				"iso_code": mmdbtype.String(countryCode),
			},
		}

		err = writer.Insert(ipNet, data)
		if err != nil {
			log.Printf("插入失败 %s: %v", networkStr, err)
		}
		count++
	}

	outFh, err := os.Create(dstFile)
	if err != nil {
		log.Fatalf("创建输出文件失败: %v", err)
	}
	defer outFh.Close()

	_, err = writer.WriteTo(outFh)
	if err != nil {
		log.Fatalf("写入 MMDB 失败: %v", err)
	}

	log.Printf("转换成功！共处理 %d 条记录，生成文件: %s\n", count, dstFile)
}
