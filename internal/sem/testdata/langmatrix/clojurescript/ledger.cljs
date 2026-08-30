(ns fixtures.ledger)

(defrecord Ledger [total])

(defn ledger-add [ledger amount]
  (+ (:total ledger) amount))

(defn ledger-double [amount]
  (* 2 (ledger-add (->Ledger 0) amount)))
