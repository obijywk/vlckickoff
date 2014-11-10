'use strict';

var app = angular.module('vlckickoffApp', [
  'ngResource', 'ng.deviceDetector'
]).config([
  '$compileProvider', function ($compileProvider) {
    $compileProvider.aHrefSanitizationWhitelist(/^\s*(https?|ftp|mailto|intent|javascript):/);
  }
]);

app.factory('Stream', [
  '$resource', function ($resource) {
    return $resource('streams/:Name', {}, {}, {});
  }
]);

app.factory('Settings', [
  '$resource', function ($resource) {
    return $resource('settings/', {}, {}, {});
  }
]);

app.controller('StreamListCtrl', [
  '$scope', 'Stream', 'Settings', 'detectUtils',
  function ($scope, Stream, Settings, detectUtils) {
    $scope.settings = Settings.get(function() {
      $scope.videoRes = $scope.settings.VideoWidth + 'x' + $scope.settings.VideoHeight;
      if (detectUtils.isAndroid()) {
        $scope.watchUrl = "intent://" +
          $scope.settings.ExternalHost +
          ":" +
          $scope.settings.StreamPort +
          "/#Intent;scheme=http;type=video/mp2t;end";
      } else {
        $scope.watchUrl = "javascript:$('#watchModal').modal()";
      }
    });
    $scope.streams = Stream.query();

    $scope.activeStream = function() {
      var active = {active: null};
      angular.forEach($scope.streams, function(stream) {
        if (stream.Active) {
          this.active = stream;
        }
      }, active);
      return active.active;
    }

    $scope.switchStream = function(newActiveStream) {
      angular.forEach($scope.streams, function(stream) {
        stream.Active = stream == newActiveStream;
        stream.$save();
      });
    }

    $scope.changeSettings = function() {
      var res = $scope.videoRes.split('x');
      $scope.settings.VideoWidth = parseInt(res[0], 10);
      $scope.settings.VideoHeight = parseInt(res[1], 10);
      $scope.settings.$save();
    }
  }
]);
