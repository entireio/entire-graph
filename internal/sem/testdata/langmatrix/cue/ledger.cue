package fixtures

#Ledger: {
	total: int
	name:  string
}

ledger: #Ledger & {
	total: 0
	name:  "primary"
}

#Add: {
	amount: int
	out:    int
}
