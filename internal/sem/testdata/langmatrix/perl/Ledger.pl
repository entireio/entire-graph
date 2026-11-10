package Ledger;

sub new {
    my ($class) = @_;
    return bless { total => 0 }, $class;
}

sub add {
    my ($self, $amount) = @_;
    return $self->{total} + $amount;
}

package main;

sub ledger_double {
    my ($amount) = @_;
    my $ledger = Ledger->new();
    return $ledger->add($amount) * 2;
}

1;
