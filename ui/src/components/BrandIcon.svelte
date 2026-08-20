<script lang="ts">
  export let name: string;
  export let size = 18;
  export let label = "";
  export let className = "";

  $: symbolId = name.startsWith("bk-") ? name : `bk-${name}`;
  $: symbolHref = `/brand/icons/sprite.svg#${symbolId}`;
</script>

<svg
  class="brand-icon {className}"
  width={size}
  height={size}
  viewBox="0 0 20 20"
  fill="none"
  role={label ? "img" : undefined}
  aria-label={label || undefined}
  aria-hidden={label ? undefined : "true"}
>
  <use href={symbolHref} />
</svg>

<style>
  .brand-icon {
    display: block;
    flex: 0 0 auto;
    color: currentColor;
  }
</style>
