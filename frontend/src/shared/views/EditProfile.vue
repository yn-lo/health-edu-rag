<script setup lang="ts">
/**
 * EditProfile 编辑个人资料页
 *
 * 后端契约：PATCH /api/auth/profile { phone, date_of_birth, gender, emergency_contact, emergency_phone }
 * 双端共享：患者端 (/chat/profile/edit) 与医护端 (/staff/profile/edit) 复用
 */
import { ref, reactive, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { Phone, Calendar, User, Heart, PhoneCall, ChevronDown } from '@lucide/vue'
import { useDsToast } from '@/shared/composables/useDsToast'
import { PageShell, AppHeader, DsErrorAlert } from '@/shared/components'
import { useAuthStore } from '@/stores/auth'
import { errmsg } from '@/shared/api/client'

const router = useRouter()
const authStore = useAuthStore()
const { showFailToast, showSuccessToast } = useDsToast()

const GENDER_OPTIONS = [
  { value: '', label: '未设置' },
  { value: 'male', label: '男' },
  { value: 'female', label: '女' },
  { value: 'other', label: '其他' },
] as const

const phone = ref('')
const dateOfBirth = ref('')
const gender = ref('')
const emergencyContact = ref('')
const emergencyPhone = ref('')
const loading = ref(false)
const errorMsg = ref('')
/** 是否已成功拉取完整资料；未就绪时禁止提交，避免用空值覆盖已有资料。 */
const profileReady = ref(false)
/** 用户手动改过的字段：重试回填时不得覆盖（包括"用户主动清空"的意图）。 */
const touched = reactive({
  phone: false,
  dateOfBirth: false,
  gender: false,
  emergencyContact: false,
  emergencyPhone: false,
})

/** 仅填充未触碰且仍为空的字段：首载/重试期间用户可能已手动输入或清空，不得覆盖。 */
function fillMissingFromUser() {
  const u = authStore.user
  if (!u) return
  if (!touched.phone && !phone.value) phone.value = u.phone ?? ''
  if (!touched.dateOfBirth && !dateOfBirth.value) dateOfBirth.value = (u.date_of_birth ?? '').slice(0, 10)
  if (!touched.gender && !gender.value) gender.value = u.gender ?? ''
  if (!touched.emergencyContact && !emergencyContact.value) emergencyContact.value = u.emergency_contact ?? ''
  if (!touched.emergencyPhone && !emergencyPhone.value) emergencyPhone.value = u.emergency_phone ?? ''
}

async function loadProfile(): Promise<boolean> {
  try {
    await authStore.fetchProfile()
    profileReady.value = true
    return true
  } catch {
    return false
  }
}

onMounted(async () => {
  // 登录响应不含完整资料（phone/生日/紧急联系人）；必须先拉取 profile，
  // 否则表单以空值提交会把已有资料覆盖为空。
  const ok = await loadProfile()
  if (!authStore.user) {
    // 双 MPA 共用本页，staff 路由表没有 login 命名路由；直接跳统一登录页。
    // ponytail:allow-location
    window.location.assign('/login')
    return
  }
  fillMissingFromUser()
  if (!ok) {
    errorMsg.value = '资料加载失败，保存前会重试；如持续失败请返回后重进'
  }
})

function goBack() {
  router.back()
}

async function handleSubmit() {
  errorMsg.value = ''
  // 资料未成功加载时先重试；仍失败则不提交（PATCH 全字段，空值会清空已有资料）。
  if (!profileReady.value) {
    loading.value = true
    if (!(await loadProfile())) {
      loading.value = false
      errorMsg.value = '无法加载当前资料，暂不能保存。请检查网络后重试'
      showFailToast(errorMsg.value)
      return
    }
    fillMissingFromUser()
  }
  loading.value = true
  try {
    await authStore.updateProfile({
      phone: phone.value,
      // 后端 DateOfBirth 为 RFC3339 time.Time；用 UTC 零点避免本地时区导致日期回退一天。
      date_of_birth: dateOfBirth.value ? `${dateOfBirth.value}T00:00:00Z` : null,
      gender: gender.value,
      emergency_contact: emergencyContact.value,
      emergency_phone: emergencyPhone.value,
    })
    showSuccessToast('个人资料已更新')
    router.back()
  } catch (e) {
    errorMsg.value = errmsg(e, '保存失败，请稍后重试')
    showFailToast(errorMsg.value)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <PageShell :bottom-nav="false" :padded="false" background="var(--bg-base-secondary)">
    <AppHeader title="编辑资料" @back="goBack" />

    <div class="flex flex-col gap-[var(--spacer-24)] px-[var(--spacer-16)] pt-[var(--spacer-24)] pb-[var(--spacer-32)]">
      <!-- 标题区 -->
      <div class="flex flex-col gap-[var(--spacer-8)]">
        <h1 class="font-heading text-heading-lg font-semibold text-text">编辑个人资料</h1>
        <p class="text-body-base text-text-secondary">完善个人资料，获得更精准的健康宣教内容</p>
      </div>

      <!-- 表单卡片 -->
      <div class="flex flex-col gap-[var(--spacer-20)] rounded-[var(--radius-card-large)] bg-[var(--bg-base-default)] p-[var(--spacer-24)] border border-[var(--border-neutral-l1)] shadow-[var(--shadow-sm)]">
        <form class="flex flex-col gap-[var(--spacer-16)]" @submit.prevent="handleSubmit">
          <!-- 手机号 -->
          <div class="flex flex-col gap-[var(--spacer-8)]">
            <label class="text-body-sm font-medium text-text-secondary">手机号</label>
            <div class="ds-field-wrap ds-field-wrap--secondary">
              <Phone class="h-4 w-4 shrink-0 text-icon-tertiary" />
              <input
                v-model="phone"
                type="tel"
                placeholder="请输入手机号"
                maxlength="20"
                @input="touched.phone = true"
                autocomplete="tel"
                aria-label="手机号"
              />
            </div>
          </div>

          <!-- 出生日期 -->
          <div class="flex flex-col gap-[var(--spacer-8)]">
            <label class="text-body-sm font-medium text-text-secondary">出生日期</label>
            <div class="ds-field-wrap ds-field-wrap--secondary">
              <Calendar class="h-4 w-4 shrink-0 text-icon-tertiary" />
              <input
                v-model="dateOfBirth"
                type="date"
                aria-label="出生日期"
                @input="touched.dateOfBirth = true"
              />
            </div>
          </div>

          <!-- 性别 -->
          <div class="flex flex-col gap-[var(--spacer-8)]">
            <label class="text-body-sm font-medium text-text-secondary">性别</label>
            <div class="ds-field-wrap ds-field-wrap--secondary relative">
              <User class="h-4 w-4 shrink-0 text-icon-tertiary" />
              <select
                v-model="gender"
                class="w-full bg-transparent text-body-base text-text appearance-none outline-none"
                aria-label="性别"
                @change="touched.gender = true"
              >
                <option
                  v-for="opt in GENDER_OPTIONS"
                  :key="opt.value"
                  :value="opt.value"
                >{{ opt.label }}</option>
              </select>
              <ChevronDown class="pointer-events-none absolute right-3 h-4 w-4 shrink-0 text-icon-tertiary" />
            </div>
          </div>

          <!-- 紧急联系人 -->
          <div class="flex flex-col gap-[var(--spacer-8)]">
            <label class="text-body-sm font-medium text-text-secondary">紧急联系人</label>
            <div class="ds-field-wrap ds-field-wrap--secondary">
              <Heart class="h-4 w-4 shrink-0 text-icon-tertiary" />
              <input
                v-model="emergencyContact"
                type="text"
                placeholder="请输入紧急联系人姓名"
                maxlength="64"
                @input="touched.emergencyContact = true"
                aria-label="紧急联系人"
              />
            </div>
          </div>

          <!-- 紧急联系电话 -->
          <div class="flex flex-col gap-[var(--spacer-8)]">
            <label class="text-body-sm font-medium text-text-secondary">紧急联系电话</label>
            <div class="ds-field-wrap ds-field-wrap--secondary">
              <PhoneCall class="h-4 w-4 shrink-0 text-icon-tertiary" />
              <input
                v-model="emergencyPhone"
                type="tel"
                placeholder="请输入紧急联系电话"
                maxlength="20"
                @input="touched.emergencyPhone = true"
                autocomplete="tel"
                aria-label="紧急联系电话"
              />
            </div>
          </div>

          <!-- 错误提示 -->
          <DsErrorAlert v-if="errorMsg" :message="errorMsg" />

          <button
            type="submit"
            class="ds-btn ds-btn--primary ds-btn--block"
            :class="{ 'ds-btn--loading': loading }"
            :disabled="loading"
          >
            <span v-if="loading" class="ds-btn__spinner" />
            保存
          </button>
        </form>
      </div>
    </div>
  </PageShell>
</template>
