package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strings"

	logs "github.com/sirupsen/logrus"
	"gopkg.in/jcmturner/gokrb5.v7/client"
	"gopkg.in/jcmturner/gokrb5.v7/config"
	"gopkg.in/jcmturner/gokrb5.v7/credentials"
)

// helper functions for handlers module
//
// Copyright (c) 2019 - Valentin Kuznetsov <vkuznet@gmail.com>
//

// helper function to extract username from auth-session cookie
func username(r *http.Request) (string, error) {
	cookie, err := r.Cookie("auth-session")
	if err != nil {
		return "", err
	}

	//     byteArray := decrypt([]byte(cookie.Value), Config.StoreSecret)
	//     n := bytes.IndexByte(byteArray, 0)
	//     s := string(byteArray[:n])

	s := cookie.Value

	arr := strings.Split(s, "-")
	if len(arr) != 2 {
		return "", errors.New("Unable to decript auth-session")
	}
	user := arr[0]
	return user, nil
}

// https://github.com/jcmturner/gokrb5/issues/7
func kuserFromCache(cacheFile string) (*credentials.Credentials, error) {
	cfg, err := config.Load(Config.Krb5Conf)
	ccache, err := credentials.LoadCCache(cacheFile)
	client, err := client.NewClientFromCCache(ccache, cfg)
	err = client.Login()
	if err != nil {
		return nil, err
	}
	return client.Credentials, nil

}

// helper function to perform kerberos authentication
func kuser(user, password string) (*credentials.Credentials, error) {
	cfg, err := config.Load(Config.Krb5Conf)
	if err != nil {
		msg := "reading krb5.conf fails"
		logs.WithFields(logs.Fields{
			"Error": err,
		}).Error(msg)
		return nil, err
	}
	client := client.NewClientWithPassword(user, Config.Realm, password, cfg, client.DisablePAFXFAST(true))
	err = client.Login()
	if err != nil {
		msg := "client login fails"
		logs.WithFields(logs.Fields{
			"Error": err,
		}).Error(msg)
		return nil, err
	}
	return client.Credentials, nil
}

// authentication function
func auth(r *http.Request) error {
	_, err := username(r)
	return err
}

// helper function to handle http server errors
func handleError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	logs.WithFields(logs.Fields{
		"Error": err,
	}).Error(msg)
	var templates ServerTemplates
	tmplData := make(map[string]interface{})
	tmplData["Message"] = strings.ToTitle(msg)
	tmplData["Class"] = "alert is-error is-large is-text-center"
	page := templates.Confirm(Config.Templates, tmplData)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(_top + page + _bottom))
}

// helper function to check user credentials for POST requests
func getUserCredentials(r *http.Request) (*credentials.Credentials, error) {
	var msg string
	// user didn't use web interface, we switch to POST form
	name := r.FormValue("name")
	ticket := r.FormValue("ticket")
	tmpFile, err := ioutil.TempFile("/tmp", name)
	if err != nil {
		msg = fmt.Sprintf("Unable to create tempfile: %v", err)
		return nil, errors.New(msg)
	}
	defer os.Remove(tmpFile.Name())
	_, err = tmpFile.Write([]byte(ticket))
	if err != nil {
		msg = "unable to write kerberos ticket"
		return nil, errors.New(msg)
	}
	err = tmpFile.Close()
	creds, err := kuserFromCache(tmpFile.Name())
	if err != nil {
		msg = "wrong user credentials"
		return nil, errors.New(msg)
	}
	if creds == nil {
		msg = "unable to obtain user credentials"
		return nil, errors.New(msg)
	}
	return creds, nil
}

// helper function to validate input data record
func validateData(rec Record) error {
	keys := MapKeys(rec)
	var mKeys, aKeys []string
	for k, _ := range rec {
		if InList(k, Config.MandatoryAttrs) {
			mKeys = append(mKeys, k)
		}
		if InList(k, Config.AdjustableAttrs) {
			aKeys = append(aKeys, k)
		}
	}
	sort.Sort(StringList(mKeys))
	sort.Sort(StringList(aKeys))
	if len(mKeys) != len(Config.MandatoryAttrs) {
		msg := fmt.Sprintf("List of records keys does not have all mandatory attributes")
		msg = fmt.Sprintf("%s\nList of records keys: %v", msg, keys)
		msg = fmt.Sprintf("%s\nList of mandatory attrs: %v", msg, mKeys)
		return errors.New(msg)
	}
	if len(aKeys) != len(Config.AdjustableAttrs) {
		msg := fmt.Sprintf("List of records keys does not have all adjustable attributes")
		msg = fmt.Sprintf("%s\nList of records keys: %v", msg, keys)
		msg = fmt.Sprintf("%s\nList of adjustable attrs: %v", msg, aKeys)
		return errors.New(msg)
	}
	return nil
}

// helper function to insert data into backend DB
func insertData(rec Record) error {
	if err := validateData(rec); err != nil {
		return err
	}
	// main attributes to work with
	path := rec["path"].(string)
	experiment := rec["experiment"].(string)
	processing := rec["processing"].(string)
	tier := rec["tier"].(string)

	//         files := FindFiles(path)
	files := []string{path}
	dataset := fmt.Sprintf("/%s/%s/%s", experiment, processing, tier)
	rec["dataset"] = dataset
	if len(files) > 0 {
		logs.WithFields(logs.Fields{
			"Record": rec,
			"Files":  files,
		}).Debug("input data")
		rec["path"] = files[0]
		did, err := InsertFiles(experiment, processing, tier, files)
		if err != nil {
			return err
		}
		rec["did"] = did
		records := []Record{rec}
		MongoUpsert(Config.DBName, Config.DBColl, records)
		return nil
	}
	msg := fmt.Sprintf("No files found associated with path=%s, experiment=%s, processing=%s, tier=%s", path, experiment, processing, tier)
	return errors.New(msg)
}
