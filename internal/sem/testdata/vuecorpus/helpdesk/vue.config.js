const path = require('path')

function resolveSrc(dir) {
  return path.join(__dirname, 'src', dir)
}

module.exports = {
  publicPath: process.env.NODE_ENV === 'production' ? '/helpdesk/' : '/',
  lintOnSave: process.env.NODE_ENV !== 'production',
  configureWebpack: {
    resolve: {
      alias: {
        '@': resolveSrc('')
      }
    }
  },
  chainWebpack(config) {
    config.module
      .rule('pug')
      .test(/\.pug$/)
      .use('pug-plain-loader')
      .loader('pug-plain-loader')
  },
  devServer: {
    proxy: {
      '/api': {
        target: 'http://localhost:8081',
        changeOrigin: true
      }
    }
  }
}
