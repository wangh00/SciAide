package main

import (
	"fmt"
	"log"
)

func startupErrorMessage(err error) string {
	return fmt.Sprintf("SciAide 无法启动。\n\n错误详情：\n%v", err)
}

func logStartupError(err error) {
	log.Print(err)
}
