<template>
  <section id="ledger-panel">
    <button @click="ledgerDouble(2)">double</button>
  </section>
</template>

<script lang="ts">
export class Ledger {
  total = 0;

  add(amount: number): number {
    return this.total + amount;
  }
}

export function ledgerDouble(amount: number): number {
  const ledger = new Ledger();
  return ledger.add(amount) * 2;
}
</script>
