/**
 * Issue photo fields — progressive enhancement for the "image" template field.
 *
 * The real control is the native <input type="file" multiple> rendered by
 * renderIssuePhotoField (internal/ui/issue_templates.go). Everything here is
 * optional polish: thumbnail previews, cumulative selection across several
 * picks, drag-and-drop and per-photo removal before the report is submitted.
 * Without this file the browser still submits the selected files.
 *
 * Listeners are attached to the document, so fields that arrive through an
 * HTMX swap (the asset picker changing the resolved template) work with no
 * per-field initialisation.
 */
(function () {
  if (window.__seamIssuePhotoField) return;
  window.__seamIssuePhotoField = true;

  var states = new WeakMap();

  function canBuildFileList() {
    try {
      return !!(new DataTransfer()).items;
    } catch (e) {
      return false;
    }
  }

  var supportsFileList = canBuildFileList();

  function stateFor(input) {
    var state = states.get(input);
    if (!state) {
      state = { files: Array.prototype.slice.call(input.files || []), urls: [] };
      states.set(input, state);
    }
    return state;
  }

  function syncInput(input, state) {
    if (!supportsFileList) return;
    var dt = new DataTransfer();
    state.files.forEach(function (file) { dt.items.add(file); });
    input.files = dt.files;
  }

  function sameFile(a, b) {
    return a.name === b.name && a.size === b.size && a.lastModified === b.lastModified;
  }

  function addFiles(input, fileList) {
    var state = stateFor(input);
    Array.prototype.forEach.call(fileList || [], function (file) {
      var duplicate = state.files.some(function (existing) { return sameFile(existing, file); });
      if (!duplicate) state.files.push(file);
    });
    syncInput(input, state);
    render(input);
  }

  function removeFile(input, index) {
    var state = stateFor(input);
    state.files.splice(index, 1);
    syncInput(input, state);
    render(input);
  }

  function revokeUrls(state) {
    state.urls.forEach(function (url) { URL.revokeObjectURL(url); });
    state.urls = [];
  }

  function render(input) {
    var field = input.closest('[data-issue-photo-field]');
    if (!field) return;
    var preview = field.querySelector('[data-issue-photo-preview]');
    if (!preview) return;

    var state = stateFor(input);
    revokeUrls(state);
    preview.textContent = '';

    state.files.forEach(function (file, index) {
      var url = URL.createObjectURL(file);
      state.urls.push(url);

      var item = document.createElement('div');
      item.className = 'relative';

      var img = document.createElement('img');
      img.src = url;
      img.alt = '';
      img.className = 'w-full h-24 object-cover rounded-lg border border-gray-200 dark:border-gray-700';
      img.style.cursor = 'pointer';
      img.addEventListener('click', function () { window.open(url, '_blank', 'noopener'); });

      var remove = document.createElement('button');
      remove.type = 'button';
      remove.textContent = '\u00d7';
      remove.title = 'Remove';
      remove.setAttribute('aria-label', 'Remove photo');
      remove.className = 'absolute top-1 right-1 w-6 h-6 rounded-full bg-black/60 text-white text-sm leading-none flex items-center justify-center hover:bg-black/80';
      remove.addEventListener('click', function (event) {
        event.preventDefault();
        event.stopPropagation();
        removeFile(input, index);
      });

      item.appendChild(img);
      item.appendChild(remove);
      preview.appendChild(item);
    });
  }

  document.addEventListener('change', function (event) {
    var input = event.target;
    if (!input || !input.matches || !input.matches('input[data-issue-photo-input]')) return;
    addFiles(input, input.files);
  });

  function fieldFromDropTarget(target) {
    var field = target && target.closest ? target.closest('[data-issue-photo-field]') : null;
    return field ? field.querySelector('input[data-issue-photo-input]') : null;
  }

  document.addEventListener('dragover', function (event) {
    if (!fieldFromDropTarget(event.target)) return;
    event.preventDefault();
  });

  document.addEventListener('drop', function (event) {
    var input = fieldFromDropTarget(event.target);
    if (!input) return;
    event.preventDefault();
    if (event.dataTransfer && event.dataTransfer.files) {
      addFiles(input, event.dataTransfer.files);
    }
  });
})();
