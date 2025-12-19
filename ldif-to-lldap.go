package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// --- Configuration ---
const (
	gVersion         = 0.06
	gStamp           = "251219-1345"
	defaultBaseURL   = "https://id.akua.com"
	adminUsername    = "akua"
	FallbackPassword = "ChangeMe!2025"
	Reset            = "\033[0m"
	Red              = "\033[31m"
	Green            = "\033[32m"
	Yellow           = "\033[33m"
	Blue             = "\033[34m"
)

// --- Data Structures ---
type AttrMeta struct {
	Name   string
	IsList bool
}
type Record struct {
	DN         string
	UID        string
	CN         string
	Mail       string
	IsGroup    bool
	IsUser     bool
	Attributes map[string][]string
}
type MapEntry struct {
	Target string
	Decode bool
}

// --- Globals ---
var (
	fieldMap = map[string]MapEntry{
		"jpegphoto":  {Target: "avatar", Decode: false},
		"photo":      {Target: "avatar", Decode: false},
		"givenname":  {Target: "firstname", Decode: false},
		"given_name": {Target: "firstname", Decode: false},
		"sn":         {Target: "lastname", Decode: false},
		"surname":    {Target: "lastname", Decode: false},
		"mail":       {Target: "mail", Decode: false},
		"uid":        {Target: "uid", Decode: false},
	}

	existingAttrs = make(map[string]string)
	groupNameToID = make(map[string]int)
	dnToUid       = make(map[string]string)
	fieldCount    = make(map[string]int)
	fieldBytes    = make(map[string]int)
	records       []Record
	uSchema       = map[string]*AttrMeta{}
	gSchema       = map[string]*AttrMeta{}
	verbosity     int
	client        *http.Client
	apiURL        string
	authURL       string
	baseURL       string
)

func main() {
	fmt.Printf("Version: %.2f\n", gVersion)
	v := flag.Bool("v", false, "verbose")
	urlFlag := flag.String("url", defaultBaseURL, "Base URL for LLDAP")
	sumFile := flag.String("s", "", "Output summary to file")
	flag.Parse()
	if *v {
		verbosity = 1
	}

	baseURL = *urlFlag
	apiURL = baseURL + "/api/graphql"
	authURL = baseURL + "/auth/simple/login"

	if verbosity > 0 {
		fmt.Printf("Target: %s\n", baseURL)
	}

	pass := os.Getenv("LLDAP_PASS")
	if pass == "" {
		log.Fatal("LLDAP_PASS not set")
	}
	client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	
	token, err := getJWT(adminUsername, pass)
	if err != nil {
		log.Fatal("Auth failed: ", err)
	}
	if token == "" {
		log.Fatal("Auth failed: Token is empty")
	}

	// --- PHASE 0: Introspection ---
	fmt.Println(Blue + "Pass 0: Introspection & Validation..." + Reset)
	runSelfTest(token)
	warmupSchema(token)
	warmupGroups(token)
	ensureG(token, "posixUser")
	ensureG(token, "service")

	// --- PHASE 1: Parse ---
	fmt.Println(Blue + "Pass 1: Parsing LDIF..." + Reset)
	parseLDIF()
	fmt.Printf("Parsed %d records.\n", len(records))

	// --- PHASE 2: Schema ---
	fmt.Println(Blue + "Pass 2: Schema Sync..." + Reset)
	bootstrap(token)

	// --- PHASE 3: Injection ---
	fmt.Println(Blue + "Pass 3: Injecting Objects..." + Reset)
	usersCreated := 0
	groupsCreated := 0
	linksCreated := 0

	for _, r := range records {
		if r.IsUser {
			ok, links := upsertUser(token, r)
			if ok {
				usersCreated++
				linksCreated += links
			}
		} else if r.IsGroup && r.CN != "" {
			if ensureG(token, r.CN) {
				groupsCreated++
			}
		}
	}

	// --- PHASE 4: Linking ---
	fmt.Println(Blue + "Pass 4: Relational Linking..." + Reset)
	for _, r := range records {
		if r.IsGroup {
			gn := strings.ToLower(getFirst(r.Attributes, "cn"))
			for _, u := range r.Attributes["member_uid"] {
				if link(token, u, gn) {
					linksCreated++
				}
			}
			for _, mdn := range r.Attributes["member"] {
				if uid, ok := dnToUid[strings.ToLower(mdn)]; ok {
					if link(token, uid, gn) {
						linksCreated++
					}
				}
			}
		}
	}

	printReport(usersCreated, groupsCreated, linksCreated, *sumFile)
}

// --- HELPER: Shell out to lldap_set_password ---
func execSetPassword(token, username, password string) error {
	cmd := exec.Command("/usr/local/bin/lldap_set_password",
		"--base-url", baseURL,
		"--token", token,
		"--username", username,
		"--password", password,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(output))
	}
	return nil
}

// --- SELF TEST ---
func runSelfTest(t string) {
	fmt.Print("Running self-test (Create -> Set PW -> Read -> Delete)... ")
	testID := "_import_test_user"

	// 1. Cleanup
	delQ := fmt.Sprintf(`mutation { deleteUser(userId: "%s") { ok } }`, testID)
	talk(t, delQ)

	// 2. Create
	createQ := fmt.Sprintf(`mutation { createUser(user: { id: "%s", email: "test@local.test", displayName: "Test User", firstName: "Test", lastName: "User" }) { id } }`, testID)
	res, ok := talk(t, createQ)
	if !ok {
		log.Fatalf("\n%s[SELF-TEST FAILED] Could not create test user.%s\nResponse: %s", Red, Reset, res)
	}

	// 3. Set Password
	if err := execSetPassword(t, testID, FallbackPassword); err != nil {
		log.Fatalf("\n%s[SELF-TEST FAILED] Could not set password via binary.%s\nError: %v", Red, Reset, err)
	}

	// 4. Verify
	readQ := fmt.Sprintf(`query { user(userId: "%s") { id email } }`, testID)
	res, ok = talk(t, readQ)
	if !ok || !strings.Contains(res, testID) {
		log.Fatalf("\n%s[SELF-TEST FAILED] Could not retrieve test user.%s\nResponse: %s", Red, Reset, res)
	}

	// 5. Cleanup
	_, ok = talk(t, delQ)
	if !ok {
		fmt.Printf(Yellow + "Warning: Could not delete test user.\n" + Reset)
	} else {
		fmt.Println(Green + "OK" + Reset)
	}
}

// --- Parsing ---
func parseLDIF() {
	scanner := bufio.NewScanner(os.Stdin)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 50*1024*1024)

	var cur *Record
	var logicalLine string

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, " ") {
			logicalLine += line[1:]
			continue
		}
		if logicalLine != "" {
			if cur == nil {
				cur = &Record{Attributes: make(map[string][]string)}
			}
			parseAttribute(cur, logicalLine)
		}
		if strings.TrimSpace(line) == "" {
			if cur != nil {
				finalizeRecord(cur)
				records = append(records, *cur)
				cur = nil
			}
			logicalLine = ""
			continue
		}
		logicalLine = line
	}
	if logicalLine != "" {
		if cur == nil {
			cur = &Record{Attributes: make(map[string][]string)}
		}
		parseAttribute(cur, logicalLine)
	}
	if cur != nil {
		finalizeRecord(cur)
		records = append(records, *cur)
	}
}

func parseAttribute(r *Record, line string) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) < 2 {
		return
	}
	rawKey := parts[0]
	val := parts[1]
	isB64 := false

	if strings.HasPrefix(val, ":") {
		isB64 = true
		val = strings.TrimPrefix(val, ":")
	}
	val = strings.TrimSpace(val)

	lowerKey := strings.ToLower(rawKey)
	targetKey := resolveAttributeName(rawKey)

	if mapping, ok := fieldMap[lowerKey]; ok {
		targetKey = mapping.Target
		if isB64 && mapping.Decode {
			d, _ := base64.StdEncoding.DecodeString(val)
			val = string(d)
		}
	} else if isB64 {
		d, _ := base64.StdEncoding.DecodeString(val)
		val = string(d)
	}

	switch targetKey {
	case "dn":
		r.DN = val
	case "uid":
		r.UID = val
	case "cn":
		r.CN = val
	case "mail":
		r.Mail = val
	}

	r.Attributes[targetKey] = append(r.Attributes[targetKey], val)
	fieldCount[targetKey]++
	fieldBytes[targetKey] += len(val)
}

func finalizeRecord(r *Record) {
	classes := append(r.Attributes["object_class"], r.Attributes["objectclass"]...)
	for _, c := range classes {
		lc := strings.ToLower(c)
		if lc == "posixgroup" {
			r.IsGroup = true
		}
		if lc == "inetorgperson" || lc == "posixaccount" || lc == "simplesecurityobject" || lc == "shadowaccount" || lc == "person" {
			r.IsUser = true
		}
	}
	if r.UID != "" && !r.IsGroup {
		r.IsUser = true
	}

	tS := uSchema
	if r.IsGroup {
		tS = gSchema
	}
	for k, v := range r.Attributes {
		if isCore(k) {
			continue
		}
		if _, ok := tS[k]; !ok {
			tS[k] = &AttrMeta{Name: k}
		}
		if len(v) > 1 {
			tS[k].IsList = true
		}
	}

	if r.IsUser {
		effectiveID := r.UID
		if effectiveID == "" {
			effectiveID = sanitizeID(r.CN)
		}
		if effectiveID != "" {
			dnToUid[strings.ToLower(r.DN)] = strings.ToLower(effectiveID)
		}
	}
}

// --- Injection ---
func upsertUser(t string, r Record) (bool, int) {
	lldapID := r.UID
	if lldapID == "" {
		lldapID = sanitizeID(r.CN)
	}
	lldapID = strings.ToLower(lldapID)
	if lldapID == "" {
		return false, 0
	}

	isService := false
	allClasses := append(r.Attributes["object_class"], r.Attributes["objectclass"]...)
	for _, c := range allClasses {
		if strings.ToLower(c) == "simplesecurityobject" {
			isService = true
			break
		}
	}

	fmt.Printf("USER: %s", lldapID)
	if r.UID != "" {
		fmt.Print(" [posixUser]")
	}
	if isService {
		fmt.Print(" [service]")
	}
	fmt.Print("\n")

	cn := r.CN
	if cn == "" {
		cn = lldapID
	}
	gn := cn
	if v, ok := r.Attributes["firstname"]; ok && len(v) > 0 {
		gn = v[0]
	}

	ln := "Imported"
	if isService {
		ln = cn
	}
	if v, ok := r.Attributes["lastname"]; ok && len(v) > 0 {
		ln = v[0]
	}

	baseEmail := r.Mail
	if baseEmail == "" || !strings.Contains(baseEmail, "@") {
		if isService {
			safeCN := sanitizeID(cn)
			baseEmail = fmt.Sprintf("service-%s@akua.com", safeCN)
		} else {
			baseEmail = fmt.Sprintf("%s@imported.local", lldapID)
		}
	}

	finalPassword := FallbackPassword
	importPass := getFirst(r.Attributes, "userpassword")
	if len(importPass) >= 8 {
		finalPassword = importPass
	} else if len(importPass) > 0 {
		if verbosity >= 1 {
			fmt.Printf(Yellow+"  > Password too short (%d chars), using default.\n"+Reset, len(importPass))
		}
	}

	idQ, _ := json.Marshal(lldapID)
	cnQ, _ := json.Marshal(cn)
	gnQ, _ := json.Marshal(gn)
	lnQ, _ := json.Marshal(ln)

	created := false
	currentEmail := baseEmail

	// RETRY LOOP
	for attempt := 0; attempt < 100; attempt++ {
		mailQ, _ := json.Marshal(currentEmail)
		q := fmt.Sprintf(`mutation { createUser(user: { id: %s, displayName: %s, email: %s, firstName: %s, lastName: %s }) { id } }`,
			string(idQ), string(cnQ), string(mailQ), string(gnQ), string(lnQ))

		res, success := talk(t, q)
		if success {
			created = true
			break
		}

		// FIX: We must check "res" for the duplicate key error, 
		// because talk() now returns false on error.
		if strings.Contains(res, "duplicate key") {
			canonID, found := fetchCanonicalUserID(t, lldapID)
			if found {
				lldapID = canonID
				idQ, _ = json.Marshal(lldapID)
				fmt.Printf(Green+"  > Merging with existing user %s\n"+Reset, lldapID)
				created = true
				break
			}

			// Collision -> Rotate
			fmt.Printf(Yellow+"  > Email Collision: %s. Rotating...\n"+Reset, currentEmail)
			currentEmail = rotateEmail(baseEmail, ln, attempt)
			continue
		} else {
			// Real error
			if verbosity >= 1 {
				fmt.Printf(Red+"[ERR] Create failed: %s\n"+Reset, res)
			}
			return false, 0
		}
	}

	if !created {
		fmt.Printf(Red+"[ERR] Failed to create user %s after multiple attempts.\n"+Reset, lldapID)
		return false, 0
	}

	time.Sleep(50 * time.Millisecond)
	if _, found := fetchCanonicalUserID(t, lldapID); !found {
		fmt.Printf(Red+"[CRITICAL] User %s creation appeared success but cannot be found. Skipping.\n"+Reset, lldapID)
		return false, 0
	}

	// STEP 2: Password (Admin Guarded)
	if lldapID == adminUsername {
		fmt.Printf(Yellow+"  > Skipping password change for admin user '%s'\n"+Reset, lldapID)
	} else {
		if err := execSetPassword(t, lldapID, finalPassword); err != nil {
			fmt.Printf(Yellow+"  > Failed setting imported password. Retrying with Fallback...\n"+Reset)
			if err2 := execSetPassword(t, lldapID, FallbackPassword); err2 != nil {
				fmt.Printf(Red+"[PW-ERR] Failed to set fallback password for %s: %v"+Reset+"\n", lldapID, err2)
			} else {
				fmt.Printf(Green+"  > Fallback password set successfully.\n"+Reset)
			}
		}
	}

	linksAdded := 0
	if r.UID != "" {
		if link(t, lldapID, "posixUser") {
			linksAdded++
		}
	}
	if isService {
		if link(t, lldapID, "service") {
			linksAdded++
		}
	}

	for k, vs := range r.Attributes {
		if isCore(k) {
			continue
		}

		if k == "avatar" || k == "jpegphoto" {
			clean := regexp.MustCompile(`\s+`).ReplaceAllString(vs[0], "")
			if len(clean) > 50000 {
				fmt.Printf(Yellow+"  > Skipping avatar for %s: Too large (%d bytes)\n"+Reset, lldapID, len(clean))
				continue
			}
			avQ, _ := json.Marshal(clean)
			talk(t, fmt.Sprintf(`mutation { updateUser(user: { id: %s, avatar: %s }) { ok } }`, string(idQ), string(avQ)))
			continue
		}

		var valQ []byte
		if len(vs) > 1 {
			valQ, _ = json.Marshal(vs)
		} else {
			valQ, _ = json.Marshal(vs[0])
		}
		nameQ, _ := json.Marshal(k)
		talk(t, fmt.Sprintf(`mutation { updateUser(user: { id: %s, insertAttributes: { name: %s, value: %s } }) { ok } }`,
			string(idQ), string(nameQ), string(valQ)))
	}
	return true, linksAdded
}

func rotateEmail(original, surname string, attempt int) string {
	parts := strings.Split(original, "@")
	if len(parts) != 2 {
		return fmt.Sprintf("bad-email-%d@imported.local", attempt)
	}
	local, domain := parts[0], parts[1]
	if attempt == 0 {
		return fmt.Sprintf("%s.%s@%s", local, strings.ToLower(surname), domain)
	}
	suffix := attempt - 1
	return fmt.Sprintf("%s-%03d@%s", local, suffix, domain)
}

func fetchCanonicalUserID(t, requestedID string) (string, bool) {
	idQ, _ := json.Marshal(requestedID)
	q := fmt.Sprintf(`query { user(userId: %s) { id } }`, string(idQ))
	res, ok := talk(t, q)
	if !ok {
		return "", false
	}
	re := regexp.MustCompile(`"id":"([^"]+)"`)
	m := re.FindStringSubmatch(res)
	if len(m) > 1 {
		return m[1], true
	}
	return "", false
}

func ensureG(t, n string) bool {
	low := strings.ToLower(n)
	if _, ok := groupNameToID[low]; ok {
		return false
	}
	nQ, _ := json.Marshal(n)
	q := fmt.Sprintf(`mutation { createGroup(name: %s) { id } }`, string(nQ))
	res, ok := talk(t, q)
	if !ok {
		return false
	}
	re := regexp.MustCompile(`"id":(\d+)`)
	if m := re.FindStringSubmatch(res); len(m) > 1 {
		var id int
		fmt.Sscanf(m[1], "%d", &id)
		groupNameToID[low] = id
		return true
	}
	return false
}

func link(t, u, g string) bool {
	gid, ok := groupNameToID[strings.ToLower(g)]
	if !ok {
		return false
	}
	uQ, _ := json.Marshal(strings.ToLower(u))
	q := fmt.Sprintf(`mutation { addUserToGroup(userId: %s, groupId: %d) { ok } }`, string(uQ), gid)
	res, ok := talk(t, q)
	// FIX: Explicitly treat "duplicate key" here as success (idempotency)
	if !ok && strings.Contains(res, "duplicate key") {
		return true
	}
	return ok && strings.Contains(res, "true")
}

func talk(t, queryBody string) (string, bool) {
	payload := map[string]string{"query": queryBody}
	jsonPayload, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf(Red+"[NET-ERR] %v"+Reset+"\n", err)
		return "", false
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	s := string(b)

	if resp.StatusCode != 200 {
		fmt.Printf(Red+"[HTTP-%d] %s"+Reset+"\n", resp.StatusCode, s)
		if verbosity >= 1 {
			fmt.Printf(Yellow+"DEBUG PAYLOAD: %s"+Reset+"\n", string(jsonPayload))
		}
		return s, false
	}

	// FIX: STRICT ERROR CHECKING. 
	// Any error returns false. Caller decides what to do with specific messages.
	if strings.Contains(s, "errors") {
		if strings.Contains(s, "None of the records are updated") {
			// Some update queries might return this non-fatally? 
			// Safer to return false and let caller handle.
			return s, false
		}
		if verbosity >= 1 {
			fmt.Printf(Red+"[API-ERR] %s"+Reset+"\n", s)
			fmt.Printf(Yellow+"DEBUG PAYLOAD: %s"+Reset+"\n", string(jsonPayload))
		}
		return s, false
	}
	return s, true
}

func sanitizeID(input string) string {
	s := strings.ToLower(input)
	s = strings.ReplaceAll(s, " ", ".")
	reg := regexp.MustCompile("[^a-z0-9._-]+")
	return reg.ReplaceAllString(s, "")
}

func getJWT(u, p string) (string, error) {
	b, _ := json.Marshal(map[string]string{"username": u, "password": p})
	req, _ := http.NewRequest("POST", authURL, bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var r struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&r)
	return r.Token, nil
}

func resolveAttributeName(src string) string {
	low := strings.ToLower(src)
	reg := regexp.MustCompile("[^a-z0-9]+")
	normalized := reg.ReplaceAllString(low, "")
	if v, ok := existingAttrs[normalized]; ok {
		return v
	}
	return normalized
}

func isCore(k string) bool {
	return k == "dn" || k == "uid" || k == "cn" || k == "mail" || k == "object_class" || k == "objectclass" || k == "userpassword" || k == "firstname" || k == "lastname"
}

func warmupSchema(t string) {
	res, _ := talk(t, `query { schema { userSchema { attributes { name } } groupSchema { attributes { name } } } }`)
	re := regexp.MustCompile(`"name":"([^"]+)"`)
	for _, m := range re.FindAllStringSubmatch(res, -1) {
		existingAttrs[strings.ToLower(m[1])] = m[1]
	}
}

func warmupGroups(t string) {
	res, _ := talk(t, `query { groups { id displayName } }`)
	re := regexp.MustCompile(`"id":(\d+),"displayName":"([^"]+)"`)
	for _, m := range re.FindAllStringSubmatch(res, -1) {
		var id int
		fmt.Sscanf(m[1], "%d", &id)
		groupNameToID[strings.ToLower(m[2])] = id
	}
}

func bootstrap(t string) {
	ensureAttribute(t, "user", "legacydn", false)
	for _, m := range uSchema {
		ensureAttribute(t, "user", m.Name, m.IsList)
	}
	for _, m := range gSchema {
		ensureAttribute(t, "group", m.Name, m.IsList)
	}
}

func ensureAttribute(t, tgt, n string, isList bool) {
	if _, ok := existingAttrs[strings.ToLower(n)]; ok {
		return
	}
	mut := "addUserAttribute"
	if tgt == "group" {
		mut = "addGroupAttribute"
	}
	nQ, _ := json.Marshal(n)
	q := fmt.Sprintf(`mutation { %s(name: %s, attributeType: STRING, isList: %t, isVisible: true, isEditable: true) { ok } }`, mut, string(nQ), isList)
	res, ok := talk(t, q)
	if ok && !strings.Contains(res, "errors") {
		existingAttrs[strings.ToLower(n)] = n
	}
}

func getFirst(m map[string][]string, k string) string {
	if v, ok := m[k]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}

func printReport(users, groups, links int, filename string) {
	type row struct {
		name  string
		count int
		bytes int
	}
	var rows []row
	for k := range fieldCount {
		rows = append(rows, row{k, fieldCount[k], fieldBytes[k]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].bytes > rows[j].bytes })

	out := os.Stdout
	if filename != "" {
		f, err := os.Create(filename)
		if err == nil {
			out = f
			defer f.Close()
		}
	}
	fmt.Fprintf(out, "\n%-30s %-10s %-15s\n", "ATTRIBUTE", "ENTRIES", "WEIGHT (BYTES)")
	fmt.Fprintf(out, strings.Repeat("-", 65)+"\n")
	for _, r := range rows {
		fmt.Fprintf(out, "%-30s %-10d %-15d\n", r.name, r.count, r.bytes)
	}
	fmt.Fprintf(out, Yellow+"\nSummary: Parsed %d records.\nCreated: %d users, %d groups, %d links.\n"+Reset, len(records), users, groups, links)
}
