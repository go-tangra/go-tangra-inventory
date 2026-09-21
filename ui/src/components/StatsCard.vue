<script setup lang="ts">
defineProps<{ title: string; value: string | number; icon?: string; color?: string; subtitle?: string }>()
</script>

<template>
  <v-card variant="tonal" :color="color">
    <v-card-text class="d-flex align-center">
      <v-avatar v-if="icon" :color="color" variant="flat" size="44" class="me-4">
        <v-icon :icon="icon" />
      </v-avatar>
      <div>
        <div class="text-h5 font-weight-bold">{{ value }}</div>
        <div class="text-body-2 text-medium-emphasis">{{ title }}</div>
        <div v-if="subtitle" class="text-caption text-medium-emphasis">{{ subtitle }}</div>
      </div>
    </v-card-text>
  </v-card>
</template>
