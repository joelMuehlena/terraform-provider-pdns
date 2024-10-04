package provider

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func IncreaseSOASerial(current string) (string, error) {
	today := time.Now().Format("20060102")

	if !strings.HasPrefix(current, today) {
		return today + "01", nil
	}

	serialNumberString := strings.Replace(current, today, "", 1)

	serialNumber, err := strconv.Atoi(serialNumberString)
	if err != nil {
		return "", err
	}

	serialNumber += 1

	return fmt.Sprintf("%s%02d", today, serialNumber), nil
}
