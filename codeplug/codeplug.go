// Copyright 2017 Dale Farnsworth. All rights reserved.

// Dale Farnsworth
// 1007 W Mendoza Ave
// Mesa, AZ  85210
// USA
//
// dale@farnsworth.org

// This file is part of Codeplug.
//
// Codeplug is free software: you can redistribute it and/or modify
// it under the terms of version 3 of the GNU Lesser General Public
// License as published by the Free Software Foundation.
//
// Codeplug is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Codeplug.  If not, see <http://www.gnu.org/licenses/>.

// Package codeplug implements access to MD380-style codeplug files.
// It can read/update/write both .rdt files and .bin files.
package codeplug

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"unicode"
)

const fileSizeRdt = 262709
const fileSizeBin = 262144
const fileOffsetRdt = 0
const fileOffsetBin = 549

// FileType tells whether the codeplug is an rdt file or a bin file.
type FileType int

const (
	FileTypeNone FileType = iota
	FileTypeRdt
	FileTypeBin
)

// A CodeplugType contain the type of codeplug. Currently only MD380-style
// codeplugs are supported.
type CodeplugType string

// A Codeplug represents a codeplug file.
type Codeplug struct {
	filename      string
	fileSize      int
	fileOffset    int
	fileType      FileType
	id            string
	bytes         []byte
	hash          [sha256.Size]byte
	rDesc         map[RecordType]*rDesc
	codeplugType  CodeplugType
	changed       bool
	lowFrequency  float64
	highFrequency float64
	connectChange func(*Change)
	changeList    []*Change
	changeIndex   int
}

// NewCodeplug returns a Codeplug, given a filename and codeplug type.
func NewCodeplug(filename string, cpType CodeplugType) (*Codeplug, error) {
	var err error
	cp := new(Codeplug)
	cp.filename = filename
	cp.codeplugType = cpType
	cp.rDesc = make(map[RecordType]*rDesc)
	cp.changeList = []*Change{&Change{}}
	cp.changeIndex = 0

	cp.id, err = randomString(64)
	if err != nil {
		return nil, err
	}

	cp.bytes, err = cp.Open(cp.filename, cp.codeplugType)
	if err != nil {
		return nil, err
	}

	if err = cp.Revert(); err != nil {
		return nil, err
	}

	codeplugs = append(codeplugs, cp)

	return cp, nil
}

// Codeplugs return a slice containing all currently open codeplugs.
func Codeplugs() []*Codeplug {
	return codeplugs
}

// Free frees a codeplug
func (cp *Codeplug) Free() {
	for i, codeplug := range codeplugs {
		if cp == codeplug {
			codeplugs = append(codeplugs[:i], codeplugs[i+1:]...)
			for _, rd := range cp.rDesc {
				rd.codeplug = nil
			}
			break
		}
	}
}

// Open returns a Codeplug of the given type representing the given file.
// An error is returned if the file does not represent a valid codeplug of
// that type.
func (cp *Codeplug) Open(filename string, cpType CodeplugType) ([]byte, error) {
	cp.filename = filename
	cp.codeplugType = cpType

	fType, err := getFileType(filename)
	cp.fileType = fType
	if err != nil {
		return []byte{}, err
	}

	file, err := os.Open(filename)
	if err != nil {
		return []byte{}, err
	}
	defer file.Close()

	switch fType {
	case FileTypeRdt:
		cp.fileSize = fileSizeRdt
		cp.fileOffset = fileOffsetRdt

	case FileTypeBin:
		cp.fileSize = fileSizeBin
		cp.fileOffset = fileOffsetBin
	}

	cpBytes := make([]byte, fileSizeRdt)
	bytes := cpBytes[cp.fileOffset : cp.fileOffset+cp.fileSize]

	bytesRead, err := file.Read(bytes)
	if err != nil {
		cp.fileType = FileTypeNone
		return []byte{}, err
	}

	if bytesRead != cp.fileSize {
		cp.fileType = FileTypeNone
		err = fmt.Errorf("Failed to read all of %s", cp.filename)
		return []byte{}, err
	}

	cp.fileType = fType

	return cpBytes, nil
}

// Revert reverts the codeplug to its state after the most recent open or
// save operation.  An error is returned if the new codeplug state is
// invalid.
func (cp *Codeplug) Revert() error {
	cp.clearCachedListNames()

	cp.load(cp.bytes)

	if err := cp.valid(); err != nil {
		return err
	}

	cp.changed = false
	cp.hash = sha256.Sum256(cp.bytes)

	cp.changeList = []*Change{&Change{}}
	cp.changeIndex = 0

	return nil
}

// Save stores the state of the Codeplug into its file
// An error may be returned if the codeplug state is invalid.
func (cp *Codeplug) Save() error {
	return cp.SaveAs(cp.filename)
}

// SaveAs saves the state of the Codeplug into a named file.
// An error will be returned if the codeplug state is invalid.
// The named file becomes the current file associated with the codeplug.
func (cp *Codeplug) SaveAs(filename string) error {
	err := cp.SaveToFile(filename)
	if err != nil {
		return err
	}

	cp.filename = filename
	cp.changed = false
	cp.hash = sha256.Sum256(cp.bytes)

	return nil
}

// SaveToFile saves the state of the Codeplug into a named file.
// An error will be returned if the codeplug state is invalid.
// The state of the codeplug is not changed, so this
// is useful for use by an autosave function.
func (cp *Codeplug) SaveToFile(filename string) error {
	if err := cp.valid(); err != nil {
		return err
	}

	cpBytes := make([]byte, fileSizeRdt)
	copy(cpBytes, cp.bytes)
	cp.store(cpBytes)

	dir, base := filepath.Split(filename)
	tmpFile, err := ioutil.TempFile(dir, base)
	if err != nil {
		return err
	}

	if err = cp.write(tmpFile, cpBytes); err != nil {
		os.Remove(tmpFile.Name())
		return err
	}

	if err := os.Rename(tmpFile.Name(), filename); err != nil {
		return err
	}

	return nil
}

// Filename returns the path name of the file associated with the codeplug.
// This is the file named in the most recent Open or SaveAs function.
func (cp *Codeplug) Filename() string {
	return cp.filename
}

// CurrentHash returns a cryptographic hash of the current (modified) codeplug
func (cp *Codeplug) CurrentHash() [sha256.Size]byte {
	if !cp.changed {
		return cp.hash
	}

	bytes := make([]byte, fileSizeRdt)
	copy(bytes, cp.bytes)
	cp.store(bytes)

	return sha256.Sum256(bytes)
}

// Changed returns false if the codeplug state is the same as that at
// the most recent Open or Save/SaveAs operation.
func (cp *Codeplug) Changed() bool {
	if cp.changed && cp.CurrentHash() != cp.hash {
		return true
	}

	return false
}

// FileType returns the type of codeplug file (rdt or bin).
func (cp *Codeplug) FileType() FileType {
	return cp.fileType
}

// Records returns all of a codeplug's records of the given RecordType.
func (cp *Codeplug) Records(rType RecordType) []*Record {
	return cp.rDesc[rType].records
}

// Record returns the first record of a codeplug's given RecordType.
func (cp *Codeplug) Record(rType RecordType) *Record {
	return cp.Records(rType)[0]
}

// MaxRecords returns a codeplug's maximum number of records of the given
// Recordtype.
func (cp *Codeplug) MaxRecords(rType RecordType) int {
	return cp.rDesc[rType].max
}

// RecordTypes returns all of the record types of the codeplug.
func (cp *Codeplug) RecordTypes() []RecordType {
	strs := make([]string, 0, len(cp.rDesc)-1)

	for rType := range cp.rDesc {
		if rType != RtRdtHeader {
			strs = append(strs, string(rType))
		}
	}
	sort.Strings(strs)

	rTypes := make([]RecordType, len(strs))
	for i, str := range strs {
		rTypes[i] = RecordType(str)
	}

	return rTypes
}

// ID returns a string unique to the codeplug.
func (cp *Codeplug) ID() string {
	return cp.id
}

// MoveRecord moves a record from its current slice index to the given index.
func (cp *Codeplug) MoveRecord(dIndex int, r *Record) {
	sIndex := r.rIndex
	cp.RemoveRecord(r)
	if sIndex < dIndex {
		dIndex--
	}
	r.rIndex = dIndex
	cp.InsertRecord(r)
}

// InsertRecord inserts the given record into the codeplug.
// The record's index determines the slice index at which it will be inserted.
// If the name of the record matches that of an existing record,
// the name is modified to make it unique.  An error will be returned if
// the codeplug's maximum records of that type would be exceeded.
func (cp *Codeplug) InsertRecord(r *Record) error {
	rType := r.rType
	records := cp.rDesc[r.rType].records
	if len(records) >= cp.MaxRecords(rType) {
		return fmt.Errorf("too many records")
	}

	err := r.makeNameUnique(*r.ListNames())
	if err != nil {
		return err
	}

	i := r.rIndex
	records = append(records[:i], append([]*Record{r}, records[i:]...)...)

	for i, r := range records {
		r.rIndex = i
	}
	cp.rDesc[r.rType].records = records

	records[0].cachedListNames = nil
	return nil
}

// RemoveRecord removes the given record from the codeplug.
func (cp *Codeplug) RemoveRecord(r *Record) {
	rType := r.rType
	index := -1
	records := cp.rDesc[rType].records
	for i, record := range records {
		if record == r {
			index = i
			break
		}
	}
	if index < 0 || index >= len(records) {
		log.Fatal("removeRecord: bad record")
	}

	records[0].cachedListNames = nil

	deleteRecord(&records, index)

	for i, r := range records {
		r.rIndex = i
	}
	cp.rDesc[rType].records = records
}

// ConnectChange will cause the given function to be called passing
// the given change.
func (cp *Codeplug) ConnectChange(fn func(*Change)) {
	cp.connectChange = fn
}

// bytesToRecord creates a record from the given byte slice.
func (cp *Codeplug) bytesToRecord(rType RecordType, rIndex int, rBytes []byte) *Record {
	r := cp.newRecord(rType, rIndex)
	r.load(rBytes)

	return r
}

// load loads all the records into the codeplug from its file.
func (cp *Codeplug) load(cpBytes []byte) {
	rInfos := cpTypes[cp.codeplugType]

	for i := range rInfos {
		ri := &rInfos[i]
		if ri.max == 0 {
			ri.max = 1
		}

		rd := &rDesc{rInfo: ri}
		cp.rDesc[ri.rType] = rd
		rd.codeplug = cp
		rd.loadRecords(cpBytes)
	}
}

// newRecord creates and returns the address of a new record of the given type.
func (cp *Codeplug) newRecord(rType RecordType, rIndex int) *Record {
	r := new(Record)
	r.rDesc = cp.rDesc[rType]
	r.rIndex = rIndex
	m := make(map[FieldType]*fDesc)
	r.fDesc = &m

	return r
}

// valid returns nil if all fields in the codeplug are valid.
func (cp *Codeplug) valid() error {
	errStr := ""
	for _, rType := range cp.RecordTypes() {
		for _, r := range cp.Records(rType) {
			if err := r.valid(); err != nil {
				errStr += err.Error()
			}
		}
	}

	for _, f := range deferredValidFields {
		if err := f.valid(); err != nil {
			errStr += fmt.Sprintf("%s %s\n", f.FullTypeName(), err.Error())
		}
	}

	if errStr != "" {
		return fmt.Errorf("%s", errStr)
	}

	return nil
}

// getFileType return the type of the file corresponding to the given filename.
func getFileType(filename string) (FileType, error) {
	fileInfo, err := os.Stat(filename)
	if err != nil {
		if os.IsNotExist(err) {
			err = fmt.Errorf("%s: does not exist", filename)
		}
		return FileTypeNone, err
	}

	switch fileInfo.Size() {
	case fileSizeRdt:
		return FileTypeRdt, nil

	case fileSizeBin:
		return FileTypeBin, nil
	}

	err = fmt.Errorf("%s is not a valid rdt or bin file", filename)
	return FileTypeNone, err
}

// store stores all all fields of the codeplug into its byte slice.
func (cp *Codeplug) store(cpBytes []byte) {
	for _, rd := range cp.rDesc {
		for rIndex := 0; rIndex < rd.max; rIndex++ {
			if rIndex < len(rd.records) {
				offset := rd.offset + rd.size*rIndex
				recordBytes := cpBytes[offset : offset+rd.size]
				rd.records[rIndex].store(recordBytes)
			} else {
				rd.deleteRecord(rIndex, cpBytes)
			}
		}
	}
}

// write writes the codeplug's byte slice into the given file.
func (cp *Codeplug) write(file *os.File, cpBytes []byte) (err error) {
	defer func() {
		err = file.Close()
		return
	}()

	bytes := cpBytes[cp.fileOffset : cp.fileOffset+cp.fileSize]
	bytesWritten, err := file.Write(bytes)
	if err != nil {
		return err
	}

	if bytesWritten != cp.fileSize {
		return fmt.Errorf("write to %s failed", cp.filename)
	}

	return nil
}

// frequencyValid returns nil if the given frequency is valid for the
// codeplug.
func (cp *Codeplug) frequencyValid(freq float64) error {
	if cp.lowFrequency == 0 {
		fDescs := cp.rDesc[RtRdtHeader].records[0].fDesc
		s := (*fDescs)[FtLowFrequency].fields[0].String()
		cp.lowFrequency, _ = strconv.ParseFloat(s, 64)
		s = (*fDescs)[FtHighFrequency].fields[0].String()
		cp.highFrequency, _ = strconv.ParseFloat(s, 64)
	}

	if cp.lowFrequency == 0 {
		cp.lowFrequency, cp.highFrequency = cp.inferFrequencyRange()
	}

	if freq >= cp.lowFrequency && freq <= cp.highFrequency {
		return nil
	}

	r := frequencyRanges[len(frequencyRanges)-1]
	if cp.lowFrequency != r.low || cp.highFrequency != r.high {
		return fmt.Errorf("frequency out of range %+v", freq)
	}

	for i := 2; i <= 3; i++ {
		r := frequencyRanges[i]
		if freq >= r.low && freq <= r.high {
			cp.lowFrequency = r.low
			cp.highFrequency = r.high
			return nil
		}
	}

	return fmt.Errorf("frequency out of range %+v", freq)
}

// inferFrequencyRange guesses the frequency range of the codeplug
// based on the current and previous values given.
func (cp *Codeplug) inferFrequencyRange() (low float64, high float64) {
	var rang frequencyRange

	for _, record := range cp.rDesc[RtChannelInformation].records {
		f := (*record.fDesc)[FtRxFrequency].fields[0]
		rxFreq := float64(*f.value.(*frequency))
		index := -1

		for i := len(frequencyRanges) - 1; i >= 0; i-- {
			rang = frequencyRanges[i]
			if rxFreq >= rang.low && rxFreq <= rang.high {
				index = i
				break
			}
		}

		if index < 0 {
			continue
		}

		if index != len(frequencyRanges)-1 {
			return rang.low, rang.high
		}
	}

	return rang.low, rang.high
}

// frequencyRange delineates a range of frequencies
type frequencyRange struct {
	low  float64
	high float64
}

// frequencyRanges contains a list of the frequency ranges
// for the various supported codeplugs.
var frequencyRanges = []frequencyRange{
	{136.0, 174.0},
	{350.0, 400.0},
	{400.0, 480.0},
	{450.0, 520.0},
	{450.0, 480.0}, // could be either of the previous two ranges
}

// publishChange passes the given change (with any additional generated
// changes resulting from that change) to a registered function.
func (cp *Codeplug) publishChange(change *Change) {
	if cp.connectChange != nil {
		cp.connectChange(change)
	}
}

// codeplugs contains the list of open codeplugs.
var codeplugs []*Codeplug

func PrintRecord(w io.Writer, r *Record) {
	rType := r.Type()
	ind := ""
	if r.max > 1 {
		ind = fmt.Sprintf("[%d]", r.rIndex+1)
	}
	fmt.Fprintf(w, "%s%s:\n", string(rType), ind)

	for _, fType := range r.FieldTypes() {
		name := string(fType)
		for _, f := range r.Fields(fType) {
			value := quoteString(f.String())
			ind := ""
			if f.max > 1 {
				ind = fmt.Sprintf("[%d]", f.fIndex+1)
			}
			fmt.Fprintf(w, "\t%s%s: %s\n", name, ind, value)
		}
	}
}

func PrintRecordWithIndex(w io.Writer, r *Record) {
	rType := r.Type()
	ind := ""
	if r.max > 1 {
		ind = fmt.Sprintf("[%d]", r.rIndex+1)
	}
	fmt.Fprintf(w, "%s%s:", string(rType), ind)

	for _, fType := range r.FieldTypes() {
		name := string(fType)
		for _, f := range r.Fields(fType) {
			value := quoteString(f.String())
			ind := ""
			if f.max > 1 {
				ind = fmt.Sprintf("[%d]", f.fIndex+1)
			}
			fmt.Fprintf(w, " %s%s:%s", name, ind, value)
		}
	}
	fmt.Fprintln(w)
}

func quoteString(str string) string {
	quote := false
	for _, c := range str {
		if unicode.IsSpace(c) {
			quote = true
			break
		}
	}
	if quote {
		runes := []rune{}
		for _, c := range str {
			switch c {
			case '"', '\\', '\n', '\t', '\r':
				runes = append(runes, '\\')
				switch c {
				case '\n':
					c = 'n'
				case '\t':
					c = 't'
				case '\r':
					c = 'r'
				}
			}
			runes = append(runes, c)
		}
		str = string(runes)
	}
	if quote || str == "" {
		str = `"` + str + `"`
	}

	return str
}

func printRecord(w io.Writer, r *Record, rFmt string, fFmt string) {
	rType := r.Type()
	ind := ""
	if r.max > 1 {
		ind = fmt.Sprintf("[%d]", r.Index()+1)
	}
	fmt.Fprintf(w, rFmt, string(rType), ind)

	for _, fType := range r.FieldTypes() {
		name := string(fType)
		for _, f := range r.Fields(fType) {
			value := quoteString(f.String())
			ind := ""
			if f.max > 1 {
				ind = fmt.Sprintf("[%d]", f.Index()+1)
			}
			fmt.Fprintf(w, fFmt, name, ind, value)
		}
	}
}

var nameToRt map[string]RecordType
var nameToFt map[RecordType]map[string]FieldType

var bareQuoteError = fmt.Errorf("bare '\"' not allowed in field value")
var noRecordNameError = fmt.Errorf("no record name")
var noFieldNameError = fmt.Errorf("no field name")

type reader struct {
	*bufio.Reader
	pos     position
	prevPos position
}

func NewReader(ioReader io.Reader) *reader {
	rdr := new(reader)
	rdr.Reader = bufio.NewReader(ioReader)
	return rdr
}

func (rdr *reader) ReadRune() (rune, int, error) {
	r, size, err := rdr.Reader.ReadRune()
	if err != nil {
		return r, size, err
	}

	rdr.prevPos = rdr.pos

	switch r {
	case '\n':
		rdr.pos.line++
		rdr.pos.column = 0

	case '\t':
		rdr.pos.column = rdr.pos.column%8 + 8
	default:
		rdr.pos.column++
	}

	return r, size, err
}

func (rdr *reader) UnreadRune() error {
	rdr.pos = rdr.prevPos
	return rdr.Reader.UnreadRune()
}

func (rdr *reader) ReadUntil(f func(rune) bool) (string, error) {
	var err error
	var r rune
	runes := []rune{}

	for {
		r, _, err = rdr.ReadRune()
		if err != nil {
			break
		}
		if f(r) {
			rdr.UnreadRune()
			break
		}
		runes = append(runes, r)
	}

	return string(runes), err
}

func (rdr *reader) ReadWhile(f func(rune) bool) (string, error) {
	return rdr.ReadUntil(func(r rune) bool {
		return !f(r)
	})
}

func (rdr *reader) ReadEscapedUntil(f func(rune) bool) (string, error) {
	var err error
	var r rune
	runes := []rune{}

	for ; ; runes = append(runes, r) {
		r, _, err = rdr.ReadRune()
		if err != nil {
			break
		}
		if r == '\\' {
			r, _, err = rdr.ReadRune()
			if err != nil {
				break
			}
			switch r {
			case 'n':
				r = '\n'
			case 't':
				r = '\t'
			case 'r':
				r = '\r'
			}
			continue
		}
		if f(r) {
			rdr.UnreadRune()
			break
		}
	}

	return string(runes), err
}

func (rdr *reader) ReadInt() (int, error) {
	str, err := rdr.ReadWhile(unicode.IsDigit)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return 0, err
	}

	return int(n), nil
}

type position struct {
	line   int
	column int
}

type positionError struct {
	error
	pos position
}

func (e positionError) Error() string {
	line := e.pos.line + 1
	column := e.pos.column + 1
	str := e.error.Error()
	return fmt.Sprintf("line %d column %d: %s", line, column, str)
}

func (cp *Codeplug) ParseRecords(iRdr io.Reader) ([]*Record, error) {
	var err error
	rdr := NewReader(iRdr)
	records := []*Record{}

	if len(nameToRt) == 0 {
		nameToRt = make(map[string]RecordType)
		for _, rType := range cp.RecordTypes() {
			nameToRt[string(rType)] = rType
		}

		nameToFt = make(map[RecordType]map[string]FieldType)
		for _, rType := range cp.RecordTypes() {
			m := make(map[string]FieldType)
			for _, fi := range cp.rDesc[rType].fInfos {
				fType := fi.fType
				name := string(fType)
				m[name] = fType
			}
			nameToFt[rType] = m
		}
	}

	for {
		var name string
		var index int
		var r *Record
		name, index, err = parseName(rdr)
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			break
		}
		if len(name) == 0 {
			err = noRecordNameError
			break
		}
		r, err = cp.nameToRecord(name, index)
		if err != nil {
			break
		}
		for {
			if rdr.pos.column == 0 {
				break
			}
			name, index, err = parseName(rdr)
			if err != nil {
				break
			}
			if len(name) == 0 {
				err = noFieldNameError
				break
			}
			fType, ok := nameToFt[r.rType][name]
			if !ok {
				err = fmt.Errorf("bad field name: %s", name)
				return nil, err
			}

			var str string
			pos := rdr.pos
			str, err = parseValue(rdr)
			if err != nil {
				break
			}
			var f *Field
			f, err = r.NewFieldWithValue(fType, index, str)
			if err != nil {
				err = fmt.Errorf("no %s: %s", f.typeName, str)
				break
			}
			dValue, ok := f.value.(deferredValue)
			if ok {
				dValue.str = str
				dValue.pos = pos
				f.value = dValue
			}
			err = r.addField(f)
			if err != nil {
				break
			}
		}

		if err != nil {
			break
		}

		records = append(records, r)
	}

	return records, err
}

func parseName(rdr *reader) (string, int, error) {
	var err error
	pos := rdr.pos
	nType := "record"
	if pos.column != 0 {
		nType = "field"
	}
	badNameError := fmt.Errorf("bad %s name", nType)
	badIndexError := fmt.Errorf("bad %s index", nType)
	index := 0

	name, err := rdr.ReadWhile(func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	})

	if err != nil {
		if err != io.EOF {
			err = positionError{badNameError, pos}
		}
		return name, 0, err
	}
	if len(name) == 0 || !unicode.IsLetter([]rune(name)[0]) {
		return name, 0, positionError{badNameError, pos}
	}

	pos = rdr.pos
	r, _, err := rdr.ReadRune()
	if err != nil {
		return name, 0, positionError{badNameError, pos}
	}
	switch r {
	case ':':
	case '[':
		pos = rdr.pos
		index, err = rdr.ReadInt()
		if err != nil {
			return name, 0, positionError{badIndexError, pos}
		}
		index--
		pos = rdr.pos
		r, _, err = rdr.ReadRune()
		if r != ']' {
			return name, 0, positionError{badIndexError, pos}
		}
		pos = rdr.pos
		r, _, err = rdr.ReadRune()
		if r != ':' {
			return name, 0, positionError{badNameError, pos}
		}
	default:
		return name, 0, positionError{badIndexError, pos}
	}
	rdr.ReadWhile(unicode.IsSpace)

	return name, index, nil
}

func parseValue(rdr *reader) (string, error) {
	pos := rdr.pos

	r, _, err := rdr.ReadRune()
	if err != nil {
		return "", positionError{err, pos}
	}
	termFunc := unicode.IsSpace
	if r == '"' {
		termFunc = func(r rune) bool {
			return r == '"'
		}
	} else {
		rdr.UnreadRune()
	}

	value, err := rdr.ReadEscapedUntil(termFunc)
	if err != nil {
		return value, positionError{err, pos}
	}
	rdr.ReadRune()

	rdr.ReadWhile(unicode.IsSpace)

	return value, nil
}

func (cp *Codeplug) nameToRecord(name string, index int) (*Record, error) {
	rType, ok := nameToRt[name]
	if !ok {
		return nil, fmt.Errorf("unknown record type: %s", name)
	}

	found := false
	for rt := range cp.rDesc {
		if rType == rt {
			found = true
			break
		}
	}
	if !found {
		name := string(rType)
		return nil, fmt.Errorf("codeplug has no record: %s", name)
	}

	return cp.newRecord(rType, index), nil
}

func (cp *Codeplug) ExportTo(filename string) (err error) {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer func() {
		err = file.Close()
		return
	}()

	w := bufio.NewWriter(file)
	for i, rType := range cp.RecordTypes() {
		for j, r := range cp.Records(rType) {
			if i != 0 || j != 0 {
				fmt.Fprintln(w)
			}
			PrintRecord(w, r)
		}
	}
	w.Flush()

	return nil
}

func (cp *Codeplug) clearCachedListNames() {
	for _, rd := range cp.rDesc {
		rd.cachedListNames = nil
	}
}

func (cp *Codeplug) ImportFrom(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	cpBytes := make([]byte, fileSizeRdt)
	copy(cpBytes, cp.bytes)
	cp.store(cpBytes)

	for _, rType := range cp.RecordTypes() {
		records := cp.Records(rType)
		for i := len(records) - 1; i >= 0; i-- {
			cp.RemoveRecord(records[i])
		}
	}

	records, err := cp.ParseRecords(file)
	if err != nil {
		cp.load(cpBytes)
		return err
	}

	for _, r := range records {
		r.rIndex = len(cp.Records(r.rType))
		cp.InsertRecord(r)
	}

	err, f := updateDeferredFields(records)
	if err != nil {
		cp.load(cpBytes)
		dValue := f.value.(deferredValue)
		err = fmt.Errorf("no %s: %s", f.typeName, dValue.str)
		return positionError{err, dValue.pos}
	}

	for _, rd := range cp.rDesc {
		if len(rd.records) == 0 {
			cp.load(cpBytes)
			rtName := string(rd.rType)
			err := fmt.Errorf("no %s records found", rtName)
			return err
		}
	}
	cp.changeList = []*Change{&Change{}}
	cp.changeIndex = 0
	cp.changed = true

	return nil
}
