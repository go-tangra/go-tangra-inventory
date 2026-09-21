<script setup lang="ts">
import { computed } from 'vue'

// A compact key/value table. Empty/undefined values are dropped so partially
// collected inventory sections stay tidy.
type Cell = string | number | boolean | null | undefined
const props = defineProps<{ title?: string; rows: [string, Cell][] }>()

const shown = computed(() => props.rows.filter(([, v]) => v !== undefined && v !== null && v !== ''))
</script>

<template>
  <v-card variant="tonal" class="mb-4">
    <v-card-title v-if="title" class="text-subtitle-1">{{ title }}</v-card-title>
    <v-card-text v-if="shown.length" class="pt-0">
      <v-table density="compact">
        <tbody>
          <tr v-for="[label, value] in shown" :key="label">
            <td class="text-medium-emphasis" style="width: 40%">{{ label }}</td>
            <td>{{ value }}</td>
          </tr>
        </tbody>
      </v-table>
    </v-card-text>
    <v-card-text v-else class="text-medium-emphasis">Not collected.</v-card-text>
  </v-card>
</template>
