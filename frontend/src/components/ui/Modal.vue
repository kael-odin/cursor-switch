<script setup>
import { ref, watch, nextTick, onBeforeUnmount } from "vue";
import Button from "@/components/ui/Button.vue";

const props = defineProps({
  visible: { type: Boolean, default: false },
  title: { type: String, default: "提示" },
  content: { type: String, default: "" },
  confirmText: { type: String, default: "确定" },
  cancelText: { type: String, default: "取消" },
  showCancel: { type: Boolean, default: true },
  confirmDisabled: { type: Boolean, default: false },
});

const emit = defineEmits(["update:visible", "confirm", "cancel"]);

const dialogRef = ref(null);
let lastFocused = null;

function handleConfirm() {
  emit("confirm");
  emit("update:visible", false);
}

function handleCancel() {
  emit("cancel");
  emit("update:visible", false);
}

function onMaskClick() {
  handleCancel();
}

// ponytail: 单模态应用，同一时刻只有一个 trap 监听；若未来出现堆叠模态，需把 trap 限定到最顶层。
function focusableEls(root) {
  if (!root) return [];
  const nodes = root.querySelectorAll('button, a[href], input, select, textarea, [tabindex]:not([tabindex="-1"])');
  return [...nodes].filter((el) => !el.hasAttribute("disabled") && el.offsetParent !== null);
}

function trapKeydown(e) {
  if (e.key === "Escape") {
    e.preventDefault();
    handleCancel();
    return;
  }
  if (e.key !== "Tab") return;
  const root = dialogRef.value;
  if (!root) return;
  const els = focusableEls(root);
  if (els.length === 0) {
    e.preventDefault();
    return;
  }
  const first = els[0];
  const last = els[els.length - 1];
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault();
    first.focus();
  }
}

watch(
  () => props.visible,
  async (v) => {
    if (v) {
      lastFocused = document.activeElement;
      await nextTick();
      dialogRef.value?.addEventListener("keydown", trapKeydown);
      const els = focusableEls(dialogRef.value);
      (els[0] ?? dialogRef.value)?.focus();
    } else {
      dialogRef.value?.removeEventListener("keydown", trapKeydown);
      lastFocused?.focus?.();
      lastFocused = null;
    }
  }
);

onBeforeUnmount(() => {
  dialogRef.value?.removeEventListener("keydown", trapKeydown);
  lastFocused?.focus?.();
});
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-mask">
      <div
        v-show="visible"
        class="modal-mask-layer fixed inset-0 z-999 flex items-center justify-center bg-black/50 p-4 "
        @click.self="onMaskClick"
      >
        <Transition name="modal-content">
          <div
            v-show="visible"
            ref="dialogRef"
            role="dialog"
            aria-modal="true"
            aria-labelledby="modal-title"
            tabindex="-1"
            class="relative z-10 w-full max-w-[360px] overflow-hidden rounded-[8px] p-px shadow-[0_25px_50px_-12px_rgba(0,0,0,0.6)]"
            style="background: linear-gradient(to bottom, #656565 0%, #3A3A3A 10px, #3A3A3A 100%);"
            @click.stop
          >
            <div class="rounded-[7px] bg-[#292929] p-5">
              <h3 id="modal-title" class="mb-3 text-base font-medium text-white">
                {{ title }}
              </h3>
              <p class="mb-5 max-h-[55vh] overflow-y-auto whitespace-pre-wrap text-sm leading-relaxed text-[#a3a3a3]">
                {{ content }}
              </p>
              <div class="flex justify-end gap-2">
                <Button v-if="showCancel" variant="default" @click="handleCancel">{{ cancelText }}</Button>
                <Button variant="primary" :disabled="confirmDisabled" @click="handleConfirm">{{ confirmText }}</Button>
              </div>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.modal-mask-enter-active,
.modal-mask-leave-active {
  transition: opacity 0.25s ease, backdrop-filter 0.25s ease;
}
.modal-mask-enter-from,
.modal-mask-leave-to {
  opacity: 0;
  backdrop-filter: blur(0);
}

.modal-content-enter-active,
.modal-content-leave-active {
  transition: all 0.25s cubic-bezier(0.34, 1.56, 0.64, 1);
}
.modal-content-enter-from,
.modal-content-leave-to {
  opacity: 0;
  transform: scale(0.9) translateY(-10px);
}
</style>
