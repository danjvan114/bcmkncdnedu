package main

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"path"
	"strconv"
	"strings"
)

/* ==========================================================================
   极简 xlsx 读取（只读第一个工作表）
   --------------------------------------------------------------------------
   不引第三方库：xlsx 就是个 zip，里面 sharedStrings.xml 存字符串，
   sheet1.xml 存单元格。够用就行，公式 / 样式 / 合并单元格全部忽略。
   标准表头：序号 | 年级 | 班别 | 姓名 | 账号 | 密码
   ========================================================================== */

type xlsxRow struct {
	Account  string
	Password string
	Name     string
	Class    string
	Grade    string
}

func readXLSX(file string) ([]xlsxRow, error) {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return nil, errors.New("打不开这个文件（不是 xlsx？）")
	}
	defer zr.Close()

	var wb *zip.File
	shared := []string{}

	for _, f := range zr.File {
		switch f.Name {
		case "xl/sharedStrings.xml":
			shared = parseSharedStrings(f)
		case "xl/workbook.xml":
			wb = f
		}
	}
	sheetPath := ""
	if wb != nil {
		sheetPath = firstSheetPath(zr, wb)
	}
	if sheetPath == "" {
		// 没有 workbook.xml 就当标准结构
		sheetPath = "xl/worksheets/sheet1.xml"
	}

	var sheet *zip.File
	for _, f := range zr.File {
		if f.Name == sheetPath {
			sheet = f
			break
		}
	}
	if sheet == nil {
		return nil, errors.New("xlsx 里没找到工作表")
	}

	rows := parseSheet(sheet, shared)
	return mapRows(rows), nil
}

// firstSheetPath 从 workbook.xml + rels 里找出第一个工作表的真实路径
func firstSheetPath(zr *zip.ReadCloser, wb *zip.File) string {
	rc, err := wb.Open()
	if err != nil {
		return ""
	}
	var rid string
	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
			for _, a := range se.Attr {
				if a.Name.Local == "id" {
					rid = a.Value
				}
			}
			break
		}
	}
	rc.Close()
	if rid == "" {
		return ""
	}

	var rels *zip.File
	for _, f := range zr.File {
		if f.Name == "xl/_rels/workbook.xml.rels" {
			rels = f
			break
		}
	}
	if rels == nil {
		return ""
	}
	rr, err := rels.Open()
	if err != nil {
		return ""
	}
	defer rr.Close()

	target := ""
	dec = xml.NewDecoder(rr)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Relationship" {
			continue
		}
		var id, tgt string
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "Id":
				id = a.Value
			case "Target":
				tgt = a.Value
			}
		}
		if id == rid && tgt != "" {
			target = tgt
			break
		}
	}
	if target == "" {
		return ""
	}
	target = strings.TrimPrefix(target, "/")
	if !strings.HasPrefix(target, "xl/") {
		target = "xl/" + target
	}
	return target
}

func parseSharedStrings(f *zip.File) []string {
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()

	var out []string
	dec := xml.NewDecoder(rc)
	inSI := false
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				buf.Reset()
			case "t":
				if inSI {
					var s string
					if err := dec.DecodeElement(&s, &t); err == nil {
						buf.WriteString(s)
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "si" {
				out = append(out, buf.String())
				inSI = false
			}
		}
	}
	return out
}

type rawCell struct {
	col string
	val string
}

func parseSheet(f *zip.File, shared []string) [][]rawCell {
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()

	var rows [][]rawCell
	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "row" {
			continue
		}
		var cells []rawCell
		for {
			t, err := dec.Token()
			if err != nil {
				return rows
			}
			if ee, ok := t.(xml.EndElement); ok && ee.Name.Local == "row" {
				break
			}
			ce, ok := t.(xml.StartElement)
			if !ok || ce.Name.Local != "c" {
				continue
			}
			var ref, typ string
			for _, a := range ce.Attr {
				switch a.Name.Local {
				case "r":
					ref = a.Value
				case "t":
					typ = a.Value
				}
			}
			col := colLetters(ref)
			val := ""
			// 读 c 的子元素
			for {
				t2, err := dec.Token()
				if err != nil {
					return rows
				}
				switch tt := t2.(type) {
				case xml.StartElement:
					switch tt.Name.Local {
					case "v":
						var s string
						if err := dec.DecodeElement(&s, &tt); err == nil {
							val = s
						}
					case "t":
						var s string
						if err := dec.DecodeElement(&s, &tt); err == nil {
							val = s
						}
					}
				case xml.EndElement:
					if tt.Name.Local == "c" {
						goto cellDone
					}
				}
			}
		cellDone:
			if typ == "s" {
				if i, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && i >= 0 && i < len(shared) {
					val = shared[i]
				}
			}
			cells = append(cells, rawCell{col: col, val: strings.TrimSpace(val)})
		}
		rows = append(rows, cells)
	}
	return rows
}

// colLetters "E12" → "E"
func colLetters(ref string) string {
	var b strings.Builder
	for _, r := range ref {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			b.WriteRune(r)
		} else {
			break
		}
	}
	return strings.ToUpper(b.String())
}

// mapRows 找表头（含「账号」和「密码」的那一行），按列名取数据
func mapRows(rows [][]rawCell) []xlsxRow {
	accCol, pwdCol, nameCol, clsCol, gradeCol := "", "", "", "", ""
	headerAt := -1

	for i, cells := range rows {
		m := map[string]string{}
		for _, c := range cells {
			m[c.col] = c.val
		}
		a, p := "", ""
		for col, v := range m {
			switch {
			case strings.Contains(v, "账号") || strings.Contains(v, "帐号") || strings.Contains(v, "用户名"):
				a = col
			case strings.Contains(v, "密码") || strings.Contains(v, "口令"):
				p = col
			}
		}
		if a != "" && p != "" {
			accCol, pwdCol = a, p
			for col, v := range m {
				switch {
				case strings.Contains(v, "姓名") || v == "名字":
					nameCol = col
				case strings.Contains(v, "班"):
					clsCol = col
				case strings.Contains(v, "年级"):
					gradeCol = col
				}
			}
			headerAt = i
			break
		}
	}
	if headerAt < 0 {
		return nil
	}

	var out []xlsxRow
	for _, cells := range rows[headerAt+1:] {
		m := map[string]string{}
		for _, c := range cells {
			m[c.col] = c.val
		}
		acc := strings.TrimSpace(m[accCol])
		pwd := strings.TrimSpace(m[pwdCol])
		if acc == "" {
			continue
		}
		out = append(out, xlsxRow{
			Account:  acc,
			Password: pwd,
			Name:     m[nameCol],
			Class:    m[clsCol],
			Grade:    m[gradeCol],
		})
	}
	return out
}

// 供调试：判断文件是不是 zip（xlsx 一定是）
func looksLikeXLSX(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == ".xlsx" || ext == ".xlsm"
}
