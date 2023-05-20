package ulist

type Membership struct {
	ListInfo      // not List because we had to fetch all of them from the database in Memberships()
	Member        bool
	MemberAddress string
	Receive       bool
	Moderate      bool
	Notify        bool
	Admin         bool
	Bounces       bool
}
