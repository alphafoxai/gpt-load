<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { quotaBudgetNumbers, validQuotaBudget, type QuotaBudgetNumber } from '@shared/quota-budget'
import type { RuntimeSettingKey, SettingsResource } from '@/app/resources/settings'
import SettingRow from '@/components/config/SettingRow.vue'
import AppSwitch from '@/components/ui/AppSwitch.vue'
import AppTextInput from '@/components/ui/AppTextInput.vue'
import CompactFieldError from '@/components/ui/CompactFieldError.vue'
import {
  createSettingsDraft,
  isValidNonNegativeInteger,
  setSettingsOverride,
  type SettingsDraft,
} from './settings-patch'
import type { SettingsDraftChange } from './use-settings-controller'
const props = defineProps<{ base: SettingsResource; draft: SettingsDraft; disabled: boolean }>()
const emit = defineEmits<{ change: [change: SettingsDraftChange] }>()
const { t, n } = useI18n()
const caps = ['credential_concurrency_limit', 'credential_rpm_limit'] as const
const budgetFields = Object.keys(quotaBudgetNumbers) as QuotaBudgetNumber[]
function owned(key: RuntimeSettingKey) {
  return props.draft.overrides.has(key)
}
function pending(key: RuntimeSettingKey) {
  return !owned(key) && props.base.settings.overrides.includes(key)
}
function toggle(key: RuntimeSettingKey) {
  emit('change', {
    key,
    draft: setSettingsOverride(props.base.settings, props.draft, key, !owned(key)),
  })
}
function copy() {
  return createSettingsDraft({
    values: props.draft.values,
    overrides: [...props.draft.overrides],
    read_only: [...props.draft.readOnly],
  })
}
function setCap(key: (typeof caps)[number], value: string) {
  const draft = copy()
  draft.values[key] = value.trim() === '' ? Number.NaN : Number(value)
  emit('change', { key, draft })
}
function setBudget(key: QuotaBudgetNumber | 'enabled', value: string | boolean) {
  const draft = copy()
  if (key === 'enabled') draft.values.quota_budget.enabled = value === true
  else
    draft.values.quota_budget[key] =
      typeof value === 'string' && value.trim() !== '' ? Number(value) : Number.NaN
  emit('change', { key: 'quota_budget', draft })
}
function row(key: RuntimeSettingKey) {
  return {
    sourceLabel: t(
      props.draft.readOnly.has(key)
        ? 'settings.runtime.environmentSource'
        : owned(key)
          ? 'settings.runtime.overrideSource'
          : pending(key)
            ? 'settings.runtime.pendingRestoreSource'
            : 'settings.runtime.defaultSource',
    ),
    actionLabel: t(owned(key) ? 'settings.runtime.restoreDefault' : 'settings.runtime.override'),
    overridden: owned(key),
    pendingRestore: pending(key),
    locked: props.draft.readOnly.has(key),
    disabled: props.disabled || props.draft.readOnly.has(key),
  }
}
</script>
<template>
  <section id="settings-capacity" class="capacity-settings" tabindex="-1">
    <header>
      <h2>{{ t('accountCapacity.title') }}</h2>
      <p>{{ t('accountCapacity.description') }}</p>
    </header>
    <p>{{ t('accountCapacity.budgetHelp') }}</p>
    <SettingRow
      v-for="key in caps"
      :key="key"
      v-bind="row(key)"
      :label="t('accountCapacity.' + key)"
      :value="pending(key) ? t('settings.runtime.resetPending') : n(base.settings.values[key])"
      :help="
        t('accountCapacity.' + (key === 'credential_rpm_limit' ? 'rpmHelp' : 'concurrencyHelp'))
      "
      @toggle="toggle(key)"
    >
      <template #control>
        <CompactFieldError
          :id="'settings-value-' + key"
          :error="
            owned(key) && !isValidNonNegativeInteger(draft.values[key])
              ? t('settings.runtime.nonNegativeIntegerError')
              : undefined
          "
        >
          <template #default="{ invalid, describedBy }">
            <AppTextInput
              :id="'settings-value-' + key"
              type="number"
              :model-value="String(draft.values[key])"
              :label="t('accountCapacity.' + key)"
              min="0"
              :max="Number.MAX_SAFE_INTEGER"
              step="1"
              inputmode="numeric"
              :disabled="disabled || draft.readOnly.has(key)"
              :invalid="invalid"
              :described-by="describedBy"
              @update:model-value="setCap(key, $event)"
            />
          </template>
        </CompactFieldError>
      </template>
    </SettingRow>
    <SettingRow
      v-bind="row('quota_budget')"
      :label="t('accountCapacity.quota_budget')"
      :value="
        pending('quota_budget')
          ? t('settings.runtime.resetPending')
          : [
              t(
                base.settings.values.quota_budget.enabled
                  ? 'settings.runtime.enabled'
                  : 'settings.runtime.disabled',
              ),
              n(base.settings.values.quota_budget.five_hour_percent) +
                '% / ' +
                n(base.settings.values.quota_budget.seven_day_percent) +
                '%',
              n(base.settings.values.quota_budget.observation_max_age_seconds) +
                ' ' +
                t('settings.runtime.seconds'),
            ].join(' · ')
      "
      :help="t('accountCapacity.budgetHelp')"
      @toggle="toggle('quota_budget')"
    >
      <template #control>
        <div class="capacity-settings__budget">
          <AppSwitch
            id="settings-value-quota_budget"
            :model-value="draft.values.quota_budget.enabled"
            :label="t('accountCapacity.enabled')"
            :disabled="disabled || draft.readOnly.has('quota_budget')"
            @update:model-value="setBudget('enabled', $event)"
          />
          <div v-for="field in budgetFields" :key="field">
            <span>{{ t('accountCapacity.' + field) }}</span>
            <AppTextInput
              :id="'settings-budget-' + field"
              type="number"
              :label="t('accountCapacity.' + field)"
              :model-value="String(draft.values.quota_budget[field])"
              :min="quotaBudgetNumbers[field].min"
              :max="quotaBudgetNumbers[field].max"
              step="1"
              inputmode="numeric"
              :invalid="
                !Number.isSafeInteger(draft.values.quota_budget[field]) ||
                draft.values.quota_budget[field] < quotaBudgetNumbers[field].min ||
                draft.values.quota_budget[field] > quotaBudgetNumbers[field].max
              "
              described-by="settings-budget-error"
              :disabled="disabled || draft.readOnly.has('quota_budget')"
              @update:model-value="setBudget(field, $event)"
            />
          </div>
          <p
            v-if="owned('quota_budget') && !validQuotaBudget(draft.values.quota_budget)"
            id="settings-budget-error"
            role="alert"
          >
            {{ t('accountCapacity.budgetError') }}
          </p>
        </div>
      </template>
    </SettingRow>
  </section>
</template>
<style scoped>
.capacity-settings,
.capacity-settings__budget,
.capacity-settings__budget > div {
  display: grid;
  gap: var(--space-4);
}
.capacity-settings {
  scroll-margin-top: 76px;
}
.capacity-settings p {
  color: var(--color-text-muted);
}
</style>
