// Copyright 2014 The Gogs Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package models

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Unknwon/com"
	"github.com/nfnt/resize"

	"github.com/gogits/gogs/modules/avatar"
	"github.com/gogits/gogs/modules/base"
	"github.com/gogits/gogs/modules/git"
	"github.com/gogits/gogs/modules/log"
	"github.com/gogits/gogs/modules/setting"
)

type UserType int

const (
	INDIVIDUAL UserType = iota // Historic reason to make it starts at 0.
	ORGANIZATION
)

var (
	ErrUserOwnRepos          = errors.New("User still have ownership of repositories")
	ErrUserHasOrgs           = errors.New("User still have membership of organization")
	ErrUserAlreadyExist      = errors.New("User already exist")
	ErrUserNotExist          = errors.New("User does not exist")
	ErrUserNotKeyOwner       = errors.New("User does not the owner of public key")
	ErrEmailAlreadyUsed      = errors.New("E-mail already used")
	ErrEmailNotExist         = errors.New("E-mail does not exist")
	ErrEmailNotActivated     = errors.New("E-mail address has not been activated")
	ErrUserNameIllegal       = errors.New("User name contains illegal characters")
	ErrLoginSourceNotExist   = errors.New("Login source does not exist")
	ErrLoginSourceNotActived = errors.New("Login source is not actived")
	ErrUnsupportedLoginType  = errors.New("Login source is unknown")
)

// User represents the object of individual and member of organization.
type User struct {
	Id        int64
	LowerName string `xorm:"UNIQUE NOT NULL"`
	Name      string `xorm:"UNIQUE NOT NULL"`
	FullName  string
	// Email is the primary email address (to be used for communication).
	Email       string `xorm:"UNIQUE(s) NOT NULL"`
	HideEmail   bool
	Passwd      string `xorm:"NOT NULL"`
	LoginType   LoginType
	LoginSource int64 `xorm:"NOT NULL DEFAULT 0"`
	LoginName   string
	Type        UserType      `xorm:"UNIQUE(s)"`
	Orgs        []*User       `xorm:"-"`
	Repos       []*Repository `xorm:"-"`
	Location    string
	Website     string
	Rands       string    `xorm:"VARCHAR(10)"`
	Salt        string    `xorm:"VARCHAR(10)"`
	Created     time.Time `xorm:"CREATED"`
	Updated     time.Time `xorm:"UPDATED"`

	// Permissions.
	IsActive     bool
	IsAdmin      bool
	AllowGitHook bool

	// Avatar.
	Avatar          string `xorm:"VARCHAR(2048) NOT NULL"`
	AvatarEmail     string `xorm:"NOT NULL"`
	UseCustomAvatar bool

	// Counters.
	NumFollowers  int
	NumFollowings int
	NumStars      int
	NumRepos      int

	// For organization.
	Description string
	NumTeams    int
	NumMembers  int
	Teams       []*Team `xorm:"-"`
	Members     []*User `xorm:"-"`
}

// EmailAdresses is the list of all email addresses of a user. Can contain the
// primary email address, but is not obligatory
type EmailAddress struct {
	Id          int64
	Uid         int64  `xorm:"INDEX NOT NULL"`
	Email       string `xorm:"UNIQUE NOT NULL"`
	IsActivated bool
	IsPrimary   bool `xorm:"-"`
}

// DashboardLink returns the user dashboard page link.
func (u *User) DashboardLink() string {
	if u.IsOrganization() {
		return setting.AppSubUrl + "/org/" + u.Name + "/dashboard/"
	}
	return setting.AppSubUrl + "/"
}

// HomeLink returns the user home page link.
func (u *User) HomeLink() string {
	return setting.AppSubUrl + "/" + u.Name
}

// AvatarLink returns user gravatar link.
func (u *User) AvatarLink() string {
	switch {
	case u.UseCustomAvatar:
		return setting.AppSubUrl + "/avatars/" + com.ToStr(u.Id)
	case setting.DisableGravatar, setting.OfflineMode:
		return setting.AppSubUrl + "/img/avatar_default.jpg"
	case setting.Service.EnableCacheAvatar:
		return setting.AppSubUrl + "/avatar/" + u.Avatar
	}
	return setting.GravatarSource + u.Avatar
}

// NewGitSig generates and returns the signature of given user.
func (u *User) NewGitSig() *git.Signature {
	return &git.Signature{
		Name:  u.Name,
		Email: u.Email,
		When:  time.Now(),
	}
}

// EncodePasswd encodes password to safe format.
func (u *User) EncodePasswd() {
	newPasswd := base.PBKDF2([]byte(u.Passwd), []byte(u.Salt), 10000, 50, sha256.New)
	u.Passwd = fmt.Sprintf("%x", newPasswd)
}

// ValidtePassword checks if given password matches the one belongs to the user.
func (u *User) ValidtePassword(passwd string) bool {
	newUser := &User{Passwd: passwd, Salt: u.Salt}
	newUser.EncodePasswd()
	return u.Passwd == newUser.Passwd
}

// CustomAvatarPath returns user custom avatar file path.
func (u *User) CustomAvatarPath() string {
	return filepath.Join(setting.AvatarUploadPath, com.ToStr(u.Id))
}

// UploadAvatar saves custom avatar for user.
// FIXME: split uploads to different subdirs in case we have massive users.
func (u *User) UploadAvatar(data []byte) error {
	u.UseCustomAvatar = true

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	m := resize.Resize(200, 200, img, resize.NearestNeighbor)

	sess := x.NewSession()
	defer sess.Close()
	if err = sess.Begin(); err != nil {
		return err
	}

	if _, err = sess.Id(u.Id).AllCols().Update(u); err != nil {
		sess.Rollback()
		return err
	}

	os.MkdirAll(setting.AvatarUploadPath, os.ModePerm)
	fw, err := os.Create(u.CustomAvatarPath())
	if err != nil {
		sess.Rollback()
		return err
	}
	defer fw.Close()
	if err = jpeg.Encode(fw, m, nil); err != nil {
		sess.Rollback()
		return err
	}

	return sess.Commit()
}

// IsOrganization returns true if user is actually a organization.
func (u *User) IsOrganization() bool {
	return u.Type == ORGANIZATION
}

// IsUserOrgOwner returns true if user is in the owner team of given organization.
func (u *User) IsUserOrgOwner(orgId int64) bool {
	return IsOrganizationOwner(orgId, u.Id)
}

// IsPublicMember returns true if user public his/her membership in give organization.
func (u *User) IsPublicMember(orgId int64) bool {
	return IsPublicMembership(orgId, u.Id)
}

// GetOrganizationCount returns count of membership of organization of user.
func (u *User) GetOrganizationCount() (int64, error) {
	return x.Where("uid=?", u.Id).Count(new(OrgUser))
}

// GetRepositories returns all repositories that user owns, including private repositories.
func (u *User) GetRepositories() (err error) {
	u.Repos, err = GetRepositories(u.Id, true)
	return err
}

// GetOrganizations returns all organizations that user belongs to.
func (u *User) GetOrganizations() error {
	ous, err := GetOrgUsersByUserId(u.Id)
	if err != nil {
		return err
	}

	u.Orgs = make([]*User, len(ous))
	for i, ou := range ous {
		u.Orgs[i], err = GetUserById(ou.OrgID)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetFullNameFallback returns Full Name if set, otherwise username
func (u *User) GetFullNameFallback() string {
	if u.FullName == "" {
		return u.Name
	}
	return u.FullName
}

// IsUserExist checks if given user name exist,
// the user name should be noncased unique.
// If uid is presented, then check will rule out that one,
// it is used when update a user name in settings page.
func IsUserExist(uid int64, name string) (bool, error) {
	if len(name) == 0 {
		return false, nil
	}
	return x.Where("id!=?", uid).Get(&User{LowerName: strings.ToLower(name)})
}

// IsEmailUsed returns true if the e-mail has been used.
func IsEmailUsed(email string) (bool, error) {
	if len(email) == 0 {
		return false, nil
	}
	if has, err := x.Get(&EmailAddress{Email: email}); has || err != nil {
		return has, err
	}
	return x.Get(&User{Email: email})
}

// GetUserSalt returns a ramdom user salt token.
func GetUserSalt() string {
	return base.GetRandomString(10)
}

// CreateUser creates record of a new user.
func CreateUser(u *User) error {
	if !IsLegalName(u.Name) {
		return ErrUserNameIllegal
	}

	isExist, err := IsUserExist(0, u.Name)
	if err != nil {
		return err
	} else if isExist {
		return ErrUserAlreadyExist
	}

	isExist, err = IsEmailUsed(u.Email)
	if err != nil {
		return err
	} else if isExist {
		return ErrEmailAlreadyUsed
	}

	u.LowerName = strings.ToLower(u.Name)
	u.AvatarEmail = u.Email
	u.Avatar = avatar.HashEmail(u.AvatarEmail)
	u.Rands = GetUserSalt()
	u.Salt = GetUserSalt()
	u.EncodePasswd()

	sess := x.NewSession()
	defer sess.Close()
	if err = sess.Begin(); err != nil {
		return err
	}

	if _, err = sess.Insert(u); err != nil {
		sess.Rollback()
		return err
	} else if err = os.MkdirAll(UserPath(u.Name), os.ModePerm); err != nil {
		sess.Rollback()
		return err
	} else if err = sess.Commit(); err != nil {
		return err
	}

	// Auto-set admin for user whose ID is 1.
	if u.Id == 1 {
		u.IsAdmin = true
		u.IsActive = true
		_, err = x.Id(u.Id).UseBool().Update(u)
	}
	return err
}

// CountUsers returns number of users.
func CountUsers() int64 {
	count, _ := x.Where("type=0").Count(new(User))
	return count
}

// GetUsers returns given number of user objects with offset.
func GetUsers(num, offset int) ([]*User, error) {
	users := make([]*User, 0, num)
	err := x.Limit(num, offset).Where("type=0").Asc("id").Find(&users)
	return users, err
}

// get user by erify code
func getVerifyUser(code string) (user *User) {
	if len(code) <= base.TimeLimitCodeLength {
		return nil
	}

	// use tail hex username query user
	hexStr := code[base.TimeLimitCodeLength:]
	if b, err := hex.DecodeString(hexStr); err == nil {
		if user, err = GetUserByName(string(b)); user != nil {
			return user
		}
		log.Error(4, "user.getVerifyUser: %v", err)
	}

	return nil
}

// verify active code when active account
func VerifyUserActiveCode(code string) (user *User) {
	minutes := setting.Service.ActiveCodeLives

	if user = getVerifyUser(code); user != nil {
		// time limit code
		prefix := code[:base.TimeLimitCodeLength]
		data := com.ToStr(user.Id) + user.Email + user.LowerName + user.Passwd + user.Rands

		if base.VerifyTimeLimitCode(data, minutes, prefix) {
			return user
		}
	}
	return nil
}

// verify active code when active account
func VerifyActiveEmailCode(code, email string) *EmailAddress {
	minutes := setting.Service.ActiveCodeLives

	if user := getVerifyUser(code); user != nil {
		// time limit code
		prefix := code[:base.TimeLimitCodeLength]
		data := com.ToStr(user.Id) + email + user.LowerName + user.Passwd + user.Rands

		if base.VerifyTimeLimitCode(data, minutes, prefix) {
			emailAddress := &EmailAddress{Email: email}
			if has, _ := x.Get(emailAddress); has {
				return emailAddress
			}
		}
	}
	return nil
}

// ChangeUserName changes all corresponding setting from old user name to new one.
func ChangeUserName(u *User, newUserName string) (err error) {
	if !IsLegalName(newUserName) {
		return ErrUserNameIllegal
	}

	return os.Rename(UserPath(u.LowerName), UserPath(newUserName))
}

// UpdateUser updates user's information.
func UpdateUser(u *User) error {
	has, err := x.Where("id!=?", u.Id).And("type=?", u.Type).And("email=?", u.Email).Get(new(User))
	if err != nil {
		return err
	} else if has {
		return ErrEmailAlreadyUsed
	}

	u.LowerName = strings.ToLower(u.Name)

	if len(u.Location) > 255 {
		u.Location = u.Location[:255]
	}
	if len(u.Website) > 255 {
		u.Website = u.Website[:255]
	}
	if len(u.Description) > 255 {
		u.Description = u.Description[:255]
	}

	if u.AvatarEmail == "" {
		u.AvatarEmail = u.Email
	}
	u.Avatar = avatar.HashEmail(u.AvatarEmail)

	u.FullName = base.Sanitizer.Sanitize(u.FullName)
	_, err = x.Id(u.Id).AllCols().Update(u)
	return err
}

// FIXME: need some kind of mechanism to record failure. HINT: system notice
// DeleteUser completely and permanently deletes everything of user.
func DeleteUser(u *User) error {
	// Check ownership of repository.
	count, err := GetRepositoryCount(u)
	if err != nil {
		return errors.New("GetRepositoryCount: " + err.Error())
	} else if count > 0 {
		return ErrUserOwnRepos
	}

	// Check membership of organization.
	count, err = u.GetOrganizationCount()
	if err != nil {
		return errors.New("GetOrganizationCount: " + err.Error())
	} else if count > 0 {
		return ErrUserHasOrgs
	}

	// FIXME: check issues, other repos' commits
	// FIXME: roll backable in some point.

	// Delete all followers.
	if _, err = x.Delete(&Follow{FollowId: u.Id}); err != nil {
		return err
	}
	// Delete oauth2.
	if _, err = x.Delete(&Oauth2{Uid: u.Id}); err != nil {
		return err
	}
	// Delete all feeds.
	if _, err = x.Delete(&Action{UserId: u.Id}); err != nil {
		return err
	}
	// Delete all watches.
	if _, err = x.Delete(&Watch{UserId: u.Id}); err != nil {
		return err
	}
	// Delete all accesses.
	if _, err = x.Delete(&Access{UserID: u.Id}); err != nil {
		return err
	}
	// Delete all alternative email addresses
	if _, err = x.Delete(&EmailAddress{Uid: u.Id}); err != nil {
		return err
	}
	// Delete all SSH keys.
	keys := make([]*PublicKey, 0, 10)
	if err = x.Find(&keys, &PublicKey{OwnerId: u.Id}); err != nil {
		return err
	}
	for _, key := range keys {
		if err = DeletePublicKey(key); err != nil {
			return err
		}
	}

	// Delete user directory.
	if err = os.RemoveAll(UserPath(u.Name)); err != nil {
		return err
	}

	_, err = x.Delete(u)
	return err
}

// DeleteInactivateUsers deletes all inactivate users and email addresses.
func DeleteInactivateUsers() error {
	_, err := x.Where("is_active=?", false).Delete(new(User))
	if err == nil {
		_, err = x.Where("is_activated=?", false).Delete(new(EmailAddress))
	}
	return err
}

// UserPath returns the path absolute path of user repositories.
func UserPath(userName string) string {
	return filepath.Join(setting.RepoRootPath, strings.ToLower(userName))
}

func GetUserByKeyId(keyId int64) (*User, error) {
	user := new(User)
	has, err := x.Sql("SELECT a.* FROM `user` AS a, public_key AS b WHERE a.id = b.owner_id AND b.id=?", keyId).Get(user)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, ErrUserNotKeyOwner
	}
	return user, nil
}

func getUserById(e Engine, id int64) (*User, error) {
	u := new(User)
	has, err := e.Id(id).Get(u)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, ErrUserNotExist
	}
	return u, nil
}

// GetUserById returns the user object by given ID if exists.
func GetUserById(id int64) (*User, error) {
	return getUserById(x, id)
}

// GetUserByName returns user by given name.
func GetUserByName(name string) (*User, error) {
	if len(name) == 0 {
		return nil, ErrUserNotExist
	}
	u := &User{LowerName: strings.ToLower(name)}
	has, err := x.Get(u)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, ErrUserNotExist
	}
	return u, nil
}

// GetUserEmailsByNames returns a list of e-mails corresponds to names.
func GetUserEmailsByNames(names []string) []string {
	mails := make([]string, 0, len(names))
	for _, name := range names {
		u, err := GetUserByName(name)
		if err != nil {
			continue
		}
		mails = append(mails, u.Email)
	}
	return mails
}

// GetUserIdsByNames returns a slice of ids corresponds to names.
func GetUserIdsByNames(names []string) []int64 {
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		u, err := GetUserByName(name)
		if err != nil {
			continue
		}
		ids = append(ids, u.Id)
	}
	return ids
}

// GetEmailAddresses returns all e-mail addresses belongs to given user.
func GetEmailAddresses(uid int64) ([]*EmailAddress, error) {
	emails := make([]*EmailAddress, 0, 5)
	err := x.Where("uid=?", uid).Find(&emails)
	if err != nil {
		return nil, err
	}

	u, err := GetUserById(uid)
	if err != nil {
		return nil, err
	}

	isPrimaryFound := false
	for _, email := range emails {
		if email.Email == u.Email {
			isPrimaryFound = true
			email.IsPrimary = true
		} else {
			email.IsPrimary = false
		}
	}

	// We alway want the primary email address displayed, even if it's not in
	// the emailaddress table (yet)
	if !isPrimaryFound {
		emails = append(emails, &EmailAddress{
			Email:       u.Email,
			IsActivated: true,
			IsPrimary:   true,
		})
	}
	return emails, nil
}

func AddEmailAddress(email *EmailAddress) error {
	used, err := IsEmailUsed(email.Email)
	if err != nil {
		return err
	} else if used {
		return ErrEmailAlreadyUsed
	}

	_, err = x.Insert(email)
	return err
}

func (email *EmailAddress) Activate() error {
	email.IsActivated = true
	if _, err := x.Id(email.Id).AllCols().Update(email); err != nil {
		return err
	}

	if user, err := GetUserById(email.Uid); err != nil {
		return err
	} else {
		user.Rands = GetUserSalt()
		return UpdateUser(user)
	}
}

func DeleteEmailAddress(email *EmailAddress) error {
	has, err := x.Get(email)
	if err != nil {
		return err
	} else if !has {
		return ErrEmailNotExist
	}

	if _, err = x.Delete(email); err != nil {
		return err
	}

	return nil

}

func MakeEmailPrimary(email *EmailAddress) error {
	has, err := x.Get(email)
	if err != nil {
		return err
	} else if !has {
		return ErrEmailNotExist
	}

	if !email.IsActivated {
		return ErrEmailNotActivated
	}

	user := &User{Id: email.Uid}
	has, err = x.Get(user)
	if err != nil {
		return err
	} else if !has {
		return ErrUserNotExist
	}

	// Make sure the former primary email doesn't disappear
	former_primary_email := &EmailAddress{Email: user.Email}
	has, err = x.Get(former_primary_email)
	if err != nil {
		return err
	} else if !has {
		former_primary_email.Uid = user.Id
		former_primary_email.IsActivated = user.IsActive
		x.Insert(former_primary_email)
	}

	user.Email = email.Email
	_, err = x.Id(user.Id).AllCols().Update(user)

	return err
}

// UserCommit represents a commit with validation of user.
type UserCommit struct {
	User *User
	*git.Commit
}

// ValidateCommitWithEmail chceck if author's e-mail of commit is corresponsind to a user.
func ValidateCommitWithEmail(c *git.Commit) *User {
	u, err := GetUserByEmail(c.Author.Email)
	if err != nil {
		return nil
	}
	return u
}

// ValidateCommitsWithEmails checks if authors' e-mails of commits are corresponding to users.
func ValidateCommitsWithEmails(oldCommits *list.List) *list.List {
	emails := map[string]*User{}
	newCommits := list.New()
	e := oldCommits.Front()
	for e != nil {
		c := e.Value.(*git.Commit)

		var u *User
		if v, ok := emails[c.Author.Email]; !ok {
			u, _ = GetUserByEmail(c.Author.Email)
			emails[c.Author.Email] = u
		} else {
			u = v
		}

		newCommits.PushBack(UserCommit{
			User:   u,
			Commit: c,
		})
		e = e.Next()
	}
	return newCommits
}

// GetUserByEmail returns the user object by given e-mail if exists.
func GetUserByEmail(email string) (*User, error) {
	if len(email) == 0 {
		return nil, ErrUserNotExist
	}
	// First try to find the user by primary email
	user := &User{Email: strings.ToLower(email)}
	has, err := x.Get(user)
	if err != nil {
		return nil, err
	}
	if has {
		return user, nil
	}

	// Otherwise, check in alternative list for activated email addresses
	emailAddress := &EmailAddress{Email: strings.ToLower(email), IsActivated: true}
	has, err = x.Get(emailAddress)
	if err != nil {
		return nil, err
	}
	if has {
		return GetUserById(emailAddress.Uid)
	}

	return nil, ErrUserNotExist
}

// SearchUserByName returns given number of users whose name contains keyword.
func SearchUserByName(opt SearchOption) (us []*User, err error) {
	if len(opt.Keyword) == 0 {
		return us, nil
	}
	opt.Keyword = strings.ToLower(opt.Keyword)

	us = make([]*User, 0, opt.Limit)
	err = x.Limit(opt.Limit).Where("type=0").And("lower_name like ?", "%"+opt.Keyword+"%").Find(&us)
	return us, err
}

// Follow is connection request for receiving user notification.
type Follow struct {
	Id       int64
	UserId   int64 `xorm:"unique(follow)"`
	FollowId int64 `xorm:"unique(follow)"`
}

// FollowUser marks someone be another's follower.
func FollowUser(userId int64, followId int64) (err error) {
	sess := x.NewSession()
	defer sess.Close()
	sess.Begin()

	if _, err = sess.Insert(&Follow{UserId: userId, FollowId: followId}); err != nil {
		sess.Rollback()
		return err
	}

	rawSql := "UPDATE `user` SET num_followers = num_followers + 1 WHERE id = ?"
	if _, err = sess.Exec(rawSql, followId); err != nil {
		sess.Rollback()
		return err
	}

	rawSql = "UPDATE `user` SET num_followings = num_followings + 1 WHERE id = ?"
	if _, err = sess.Exec(rawSql, userId); err != nil {
		sess.Rollback()
		return err
	}
	return sess.Commit()
}

// UnFollowUser unmarks someone be another's follower.
func UnFollowUser(userId int64, unFollowId int64) (err error) {
	session := x.NewSession()
	defer session.Close()
	session.Begin()

	if _, err = session.Delete(&Follow{UserId: userId, FollowId: unFollowId}); err != nil {
		session.Rollback()
		return err
	}

	rawSql := "UPDATE `user` SET num_followers = num_followers - 1 WHERE id = ?"
	if _, err = session.Exec(rawSql, unFollowId); err != nil {
		session.Rollback()
		return err
	}

	rawSql = "UPDATE `user` SET num_followings = num_followings - 1 WHERE id = ?"
	if _, err = session.Exec(rawSql, userId); err != nil {
		session.Rollback()
		return err
	}
	return session.Commit()
}

func UpdateMentions(userNames []string, issueId int64) error {
	users := make([]*User, 0, len(userNames))

	if err := x.Where("name IN (?)", strings.Join(userNames, "\",\"")).OrderBy("name ASC").Find(&users); err != nil {
		return err
	}

	ids := make([]int64, 0, len(userNames))

	for _, user := range users {
		ids = append(ids, user.Id)

		if user.Type == INDIVIDUAL {
			continue
		}

		if user.NumMembers == 0 {
			continue
		}

		tempIds := make([]int64, 0, user.NumMembers)

		orgUsers, err := GetOrgUsersByOrgId(user.Id)

		if err != nil {
			return err
		}

		for _, orgUser := range orgUsers {
			tempIds = append(tempIds, orgUser.ID)
		}

		ids = append(ids, tempIds...)
	}

	if err := UpdateIssueUserPairsByMentions(ids, issueId); err != nil {
		return err
	}

	return nil
}
